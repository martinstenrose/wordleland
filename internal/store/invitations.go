package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// InvitationLifetime is how long an invitation stays good.
//
// Longer than a password reset by a wide margin: a reset is something
// somebody asked for and is waiting on, while an invitation arrives
// unannounced and may sit until the weekend.
const InvitationLifetime = 7 * 24 * time.Hour

var (
	// ErrInvitationInvalid covers every reason a token cannot be used —
	// unknown, expired, already accepted. They are one error on purpose:
	// telling the difference would let somebody probe for real tokens.
	ErrInvitationInvalid = errors.New("invitation is invalid")

	// ErrPlayerAlreadyLinked is returned when the player already has a
	// login, so there is nothing to claim.
	ErrPlayerAlreadyLinked = errors.New("that player already has a login")
)

// Invitation is an outstanding offer to claim a player.
type Invitation struct {
	ID        int64
	Email     string
	PlayerID  int64
	InvitedBy *int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time

	// Locale is the language the invitation was sent in, and the one the
	// account it creates starts in. There is no user to ask yet, so the
	// choice is made when the invitation is.
	Locale string
}

// Pending reports whether the invitation can still be accepted.
func (i Invitation) Pending(now time.Time) bool {
	return i.UsedAt == nil && now.Before(i.ExpiresAt)
}

// CreateInvitation issues a token for claiming a player.
//
// Any earlier invitation for the same player is spent first, so re-inviting
// somebody does not leave two live links to one account.
func CreateInvitation(ctx context.Context, db *sql.DB, actor Actor, playerID int64,
	email, locale string) (string, error) {

	email = NormalizeEmail(email)
	if !ValidEmail(email) {
		return "", ErrInvalidEmail
	}
	if locale == "" {
		return "", errors.New("invitation locale is empty")
	}

	var token string
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		player, err := PlayerByID(ctx, tx, playerID)
		if err != nil {
			return err
		}
		if player.UserID != nil {
			return ErrPlayerAlreadyLinked
		}

		var taken int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM users WHERE email = ?`, email).Scan(&taken); err != nil {
			return fmt.Errorf("check address: %w", err)
		}
		if taken > 0 {
			return ErrEmailTaken
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE invitations SET used_at = CURRENT_TIMESTAMP
			 WHERE player_id = ? AND used_at IS NULL`, playerID); err != nil {
			return fmt.Errorf("spend earlier invitations: %w", err)
		}

		token, err = newToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO invitations (token_hash, email, player_id, invited_by, expires_at, locale)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			HashToken(token), email, playerID, actor.UserID,
			time.Now().Add(InvitationLifetime), locale,
		); err != nil {
			return fmt.Errorf("create invitation: %w", err)
		}

		return Audit(ctx, tx, actor, ActionInvitationSent, SubjectPlayer, &playerID,
			map[string]any{"email": email, "locale": locale})
	})
	return token, err
}

// PendingInvitation returns the live invitation for a player, if any.
func PendingInvitation(ctx context.Context, q Querier, playerID int64) (Invitation, error) {
	var inv Invitation
	err := q.QueryRowContext(ctx,
		`SELECT id, email, player_id, invited_by, created_at, expires_at, used_at, locale
		 FROM invitations
		 WHERE player_id = ? AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
		 ORDER BY id DESC LIMIT 1`, playerID,
	).Scan(&inv.ID, &inv.Email, &inv.PlayerID, &inv.InvitedBy,
		&inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.Locale)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrInvitationInvalid
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("read invitation: %w", err)
	}
	return inv, nil
}

// InvitationByToken reads a token without spending it, for showing the form.
func InvitationByToken(ctx context.Context, q Querier, token string) (Invitation, Player, error) {
	var inv Invitation
	err := q.QueryRowContext(ctx,
		`SELECT id, email, player_id, invited_by, created_at, expires_at, used_at, locale
		 FROM invitations WHERE token_hash = ?`, HashToken(token),
	).Scan(&inv.ID, &inv.Email, &inv.PlayerID, &inv.InvitedBy,
		&inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.Locale)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, Player{}, ErrInvitationInvalid
	}
	if err != nil {
		return Invitation{}, Player{}, fmt.Errorf("read invitation: %w", err)
	}
	if !inv.Pending(time.Now()) {
		return Invitation{}, Player{}, ErrInvitationInvalid
	}

	player, err := PlayerByID(ctx, q, inv.PlayerID)
	if err != nil {
		return Invitation{}, Player{}, err
	}
	return inv, player, nil
}

// AcceptInvitation creates the account, links it to the player and spends
// the token — all in one transaction, so a half-accepted invitation cannot
// leave a login with no player or a player claimed by nobody.
func AcceptInvitation(ctx context.Context, db *sql.DB, token, passwordHash string) (User, error) {
	var user User
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var inv Invitation
		err := tx.QueryRowContext(ctx,
			`SELECT id, email, player_id, invited_by, created_at, expires_at, used_at, locale
			 FROM invitations WHERE token_hash = ?`, HashToken(token),
		).Scan(&inv.ID, &inv.Email, &inv.PlayerID, &inv.InvitedBy,
			&inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.Locale)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("read invitation: %w", err)
		}
		if !inv.Pending(time.Now()) {
			return ErrInvitationInvalid
		}

		player, err := PlayerByID(ctx, tx, inv.PlayerID)
		if err != nil {
			return err
		}
		if player.UserID != nil {
			return ErrPlayerAlreadyLinked
		}

		// The invited address is confirmed by the act of following the
		// link, so the account starts verified.
		user, err = createUserTx(ctx, tx, SystemActor(), inv.Email, passwordHash, false)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET email_verified_at = CURRENT_TIMESTAMP, locale = ? WHERE id = ?`,
			inv.Locale, user.ID); err != nil {
			return fmt.Errorf("mark verified: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE players SET user_id = ? WHERE id = ?`, user.ID, player.ID); err != nil {
			if isUniqueViolation(err) {
				return ErrUserLinkedElsewhere
			}
			return fmt.Errorf("link player: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE invitations SET used_at = CURRENT_TIMESTAMP WHERE id = ?`, inv.ID); err != nil {
			return fmt.Errorf("spend invitation: %w", err)
		}

		actor := PlayerActor(user.ID)
		if err := Audit(ctx, tx, actor, ActionInvitationAccepted, SubjectPlayer, &player.ID,
			map[string]any{"email": inv.Email}); err != nil {
			return err
		}
		user, err = UserByID(ctx, tx, user.ID)
		return err
	})
	return user, err
}
