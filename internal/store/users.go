package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// handleLen is the WebAuthn user handle length.
const handleLen = 32

var (
	// ErrUserNotFound is returned when no user matches.
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailTaken is returned when an address is already registered.
	ErrEmailTaken = errors.New("email address is already registered")

	// ErrInvalidEmail is returned for an address that is not one.
	ErrInvalidEmail = errors.New("that is not an email address")

	// ErrEmailUnchanged is returned when the new address is the old one,
	// which is worth saying rather than sending a pointless confirmation.
	ErrEmailUnchanged = errors.New("that is already the address on this account")
)

// User is a login.
type User struct {
	ID              int64
	Handle          []byte
	Email           string
	EmailVerifiedAt *time.Time
	DisabledAt      *time.Time
	PasswordHash    string
	IsAdmin         bool
	HasTOTP         bool

	// Locale is the language this account reads in, and the one its mail
	// is written in. A cookie cannot serve the second: a message has to be
	// composed without a request in front of it.
	Locale string

	// PendingEmail is an address waiting to be confirmed. Sign-in still
	// uses Email until it is.
	PendingEmail *string
}

// Disabled reports whether the account has been retired.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// NormalizeEmail lowercases and trims an address so lookups are stable.
// Addresses are matched case-insensitively here even though the local part is
// technically case-sensitive: no real mail provider treats it that way, and
// letting two accounts differ only by capitalisation invites confusion about
// which one someone is logging into.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

const userColumns = `id, handle, email, email_verified_at, disabled_at, password_hash, is_admin,
	totp_secret_encrypted IS NOT NULL AS has_totp, pending_email, locale`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Handle, &u.Email, &u.EmailVerifiedAt, &u.DisabledAt,
		&u.PasswordHash, &u.IsAdmin, &u.HasTOTP, &u.PendingEmail, &u.Locale)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// UserByEmail looks a user up by address.
func UserByEmail(ctx context.Context, q Querier, email string) (User, error) {
	return scanUser(q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ?`, NormalizeEmail(email)))
}

// UserByID looks a user up by id.
func UserByID(ctx context.Context, q Querier, id int64) (User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// ListUsers returns every account, ordered by address.
//
// Ordered by email rather than by id so a picker built from it is stable
// as accounts come and go, and disabled accounts are included: an admin
// choosing who to link needs to see that an account exists even when it
// cannot currently sign in.
func ListUsers(ctx context.Context, q Querier) ([]User, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUser inserts a user and returns it.
//
// The WebAuthn handle is generated now even though passkeys are deferred
// : it must be opaque and stable, and minting it later would mean
// backfilling it for accounts that already exist.
func CreateUser(ctx context.Context, db *sql.DB, actor Actor, email, passwordHash string, isAdmin bool) (User, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return User{}, errors.New("email address is empty")
	}

	handle := make([]byte, handleLen)
	if _, err := rand.Read(handle); err != nil {
		return User{}, fmt.Errorf("generate user handle: %w", err)
	}

	var user User
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var err error
		user, err = createUserTx(ctx, tx, actor, email, passwordHash, isAdmin)
		return err
	})
	return user, err
}

// createUserTx inserts a user inside a transaction the caller owns, so a
// flow that creates an account alongside other writes — accepting an
// invitation, say — commits or fails as one thing.
func createUserTx(ctx context.Context, tx *sql.Tx, actor Actor, email, passwordHash string, isAdmin bool) (User, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return User{}, errors.New("email address is empty")
	}

	handle := make([]byte, handleLen)
	if _, err := rand.Read(handle); err != nil {
		return User{}, fmt.Errorf("generate user handle: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (handle, email, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		handle, email, passwordHash, isAdmin)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}

	if err := Audit(ctx, tx, actor, ActionUserCreated, SubjectUser, &id,
		map[string]any{"email": email, "is_admin": isAdmin}); err != nil {
		return User{}, err
	}
	return UserByID(ctx, tx, id)
}

// SetPendingEmail records an address change waiting for confirmation.
//
// The account keeps signing in with the old address until the new one is
// confirmed: a typo would otherwise lock somebody out of their own account
// with no way back.
func SetPendingEmail(ctx context.Context, db *sql.DB, actor Actor, userID int64, email string) error {
	email = NormalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return ErrInvalidEmail
	}

	return InTx(ctx, db, func(tx *sql.Tx) error {
		before, err := UserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if before.Email == email {
			return ErrEmailUnchanged
		}

		var taken int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM users WHERE email = ? AND id != ?`, email, userID).Scan(&taken); err != nil {
			return fmt.Errorf("check address: %w", err)
		}
		if taken > 0 {
			return ErrEmailTaken
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET pending_email = ? WHERE id = ?`, email, userID); err != nil {
			return fmt.Errorf("set pending email: %w", err)
		}
		// The address itself is the change worth recording; it is not a
		// secret, and an admin reading the log needs to see what was asked
		// for.
		return Audit(ctx, tx, actor, ActionUserEmailPending, SubjectUser, &userID,
			map[string]any{"pending_email": email})
	})
}

// PromotePendingEmail applies a confirmed address change.
func PromotePendingEmail(ctx context.Context, tx *sql.Tx, actor Actor, userID int64) error {
	user, err := UserByID(ctx, tx, userID)
	if err != nil {
		return err
	}
	if user.PendingEmail == nil {
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET email = pending_email, pending_email = NULL,
		 email_verified_at = CURRENT_TIMESTAMP WHERE id = ?`, userID); err != nil {
		if isUniqueViolation(err) {
			return ErrEmailTaken
		}
		return fmt.Errorf("promote pending email: %w", err)
	}
	return Audit(ctx, tx, actor, ActionUserEmailChanged, SubjectUser, &userID,
		map[string]any{"email": map[string]any{"from": user.Email, "to": *user.PendingEmail}})
}

// SetUserPassword replaces a user's password hash and invalidates their
// sessions.
//
// Dropping the sessions is the point: a reset that leaves the old ones alive
// does not actually lock anyone out, which is the reason to reset in the first
// place.
func SetUserPassword(ctx context.Context, db *sql.DB, actor Actor, userID int64, passwordHash string) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrUserNotFound
		}

		deleted, err := deleteSessions(ctx, tx, userID)
		if err != nil {
			return err
		}
		return Audit(ctx, tx, actor, ActionUserPasswordReset, SubjectUser, &userID,
			map[string]any{"sessions_invalidated": deleted})
	})
}

// ResetUserTOTP clears both the enrolled and pending TOTP secrets, forcing
// re-enrolment. For an admin this is not a bypass: 2FA is mandatory for
// admins, so the next login is redirected into enrolment.
func ResetUserTOTP(ctx context.Context, db *sql.DB, actor Actor, userID int64) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE users
			SET totp_secret_encrypted = NULL,
			    totp_pending_secret_encrypted = NULL,
			    totp_last_step = NULL
			WHERE id = ?`, userID)
		if err != nil {
			return fmt.Errorf("reset 2fa: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrUserNotFound
		}

		// Recovery codes go with the secret. A code minted against the
		// old enrolment is a way straight past the new one, which would
		// make this reset no reset at all.
		if err := DiscardRecoveryCodes(ctx, tx, userID); err != nil {
			return err
		}

		// Sessions go too: one that already cleared TOTP would otherwise
		// outlive the secret it was granted against.
		deleted, err := deleteSessions(ctx, tx, userID)
		if err != nil {
			return err
		}
		return Audit(ctx, tx, actor, ActionUser2FAReset, SubjectUser, &userID,
			map[string]any{"sessions_invalidated": deleted})
	})
}

// SetUserDisabled retires or restores an account.
//
// Disabling deletes the user's sessions. Without that, a disabled account
// stays usable for the lifetime of a session it already holds — up to thirty
// days — which is not what "disabled" means to whoever ran the command.
func SetUserDisabled(ctx context.Context, db *sql.DB, actor Actor, userID int64, disabled bool) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		var (
			query  string
			action string
		)
		if disabled {
			// COALESCE so re-running disable keeps the original timestamp:
			// when they were disabled is the fact worth preserving, and
			// re-running still ends any sessions opened in between.
			query = `UPDATE users SET disabled_at = COALESCE(disabled_at, CURRENT_TIMESTAMP) WHERE id = ?`
			action = ActionUserDisabled
		} else {
			query = `UPDATE users SET disabled_at = NULL WHERE id = ?`
			action = ActionUserEnabled
		}

		res, err := tx.ExecContext(ctx, query, userID)
		if err != nil {
			return fmt.Errorf("set disabled: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrUserNotFound
		}

		detail := map[string]any{}
		if disabled {
			deleted, err := deleteSessions(ctx, tx, userID)
			if err != nil {
				return err
			}
			detail["sessions_invalidated"] = deleted
		}
		return Audit(ctx, tx, actor, action, SubjectUser, &userID, detail)
	})
}

// deleteSessions removes every session for a user and reports how many.
func deleteSessions(ctx context.Context, q Querier, userID int64) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	return n, nil
}

// isUniqueViolation reports whether err is a UNIQUE constraint failure.
//
// modernc.org/sqlite returns a driver-specific error type; matching on the
// message keeps this from depending on that package's internals, at the cost
// of being string-shaped.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

// ErrUsersExist reports that bootstrap was asked for on a database that
// already has accounts.
var ErrUsersExist = errors.New("users already exist")

// BootstrapAdmin creates the first administrator, and only the first.
//
// It is a no-op once any user exists, which is what makes it safe to leave
// configured indefinitely: it cannot resurrect an account that was
// deliberately disabled, and it cannot overwrite a password that has since
// been changed. "First" is measured across the whole installation rather
// than by email, because an email-specific check would quietly recreate an
// account someone had removed on purpose.
//
// The check and the insert share a transaction, so two app instances racing on
// a fresh volume cannot both succeed.
func BootstrapAdmin(ctx context.Context, db *sql.DB, email, passwordHash string) (User, bool, error) {
	var (
		user    User
		created bool
	)
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&existing); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if existing > 0 {
			return nil
		}

		normalized := NormalizeEmail(email)
		if normalized == "" {
			return errors.New("email address is empty")
		}

		handle := make([]byte, handleLen)
		if _, err := rand.Read(handle); err != nil {
			return fmt.Errorf("generate user handle: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (handle, email, password_hash, is_admin) VALUES (?, ?, ?, 1)`,
			handle, normalized, passwordHash)
		if err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}

		// Attributed to the system: nothing existed that could have
		// authorised it, which is the same reasoning the CLI uses for the
		// first user it creates.
		if err := Audit(ctx, tx, SystemActor(), ActionUserCreated, SubjectUser, &id,
			map[string]any{"email": normalized, "is_admin": true, "via": "bootstrap"}); err != nil {
			return err
		}

		user, err = UserByID(ctx, tx, id)
		created = true
		return err
	})
	return user, created, err
}

// SetUserLocale records the language an account reads in.
//
// Written when a signed-in reader uses the language picker, so the choice
// reaches their mail as well as their browser. An unknown value is refused
// rather than stored: the caller checks it against the catalogues it has,
// and a locale nobody has strings for would silently mean the fallback
// forever.
func SetUserLocale(ctx context.Context, q Querier, userID int64, locale string) error {
	if locale == "" {
		return errors.New("locale is empty")
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE users SET locale = ? WHERE id = ?`, locale, userID); err != nil {
		return fmt.Errorf("set user locale: %w", err)
	}
	return nil
}
