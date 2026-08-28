package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Actor kinds, matching the CHECK on audit_log.
const (
	ActorAdmin  = "admin"
	ActorToken  = "token"
	ActorPlayer = "player"
	ActorSystem = "system"
)

// Audit actions. Constants rather than literals so a typo is a compile error
// and the vocabulary stays greppable as it grows.
const (
	ActionUserCreated         = "user.created"
	ActionUserDisabled        = "user.disabled"
	ActionUserEnabled         = "user.enabled"
	ActionUserPasswordReset   = "user.password_reset"
	ActionUser2FAReset        = "user.2fa_reset"
	ActionUser2FAEnrolled     = "user.2fa_enrolled"
	ActionUserEmailPending    = "user.email_pending"
	ActionRecoveryCodesIssued = "user.recovery_codes_issued"
	ActionRecoveryCodeUsed    = "user.recovery_code_used"
	ActionUserEmailChanged    = "user.email_changed"

	ActionPlayerCreated     = "player.created"
	ActionPlayerUpdated     = "player.updated"
	ActionPlayerRetired     = "player.retired"
	ActionPlayerReactivated = "player.reactivated"
	ActionPlayerLinked      = "player.linked"
	ActionPlayerUnlinked    = "player.unlinked"

	ActionInvitationSent     = "invitation.sent"
	ActionInvitationAccepted = "invitation.accepted"

	ActionTokenCreated = "token.created"
	ActionTokenRevoked = "token.revoked"

	ActionResultCreated = "result.created"
	ActionResultUpdated = "result.updated"
	ActionResultDeleted = "result.deleted"

	ActionIdentityAdded     = "identity.added"
	ActionIdentityClaimed   = "identity.claimed"
	ActionIdentityDiscarded = "identity.discarded"

	ActionSlugGenerated = "settings.slug_generated"
	ActionSlugRotated   = "settings.slug_rotated"
)

// Subject types.
const (
	SubjectUser     = "user"
	SubjectPlayer   = "player"
	SubjectResult   = "result"
	SubjectIdentity = "identity"
	SubjectToken    = "token"
	SubjectSettings = "settings"
)

// Actor identifies who caused a change. Exactly one of UserID or TokenID is
// set, except for Kind == ActorSystem where neither is; the audit_log CHECK
// enforces the same rule at the database.
type Actor struct {
	Kind    string
	UserID  *int64
	TokenID *int64
}

// AdminActor is an administrator acting through the CLI or admin UI.
func AdminActor(userID int64) Actor { return Actor{Kind: ActorAdmin, UserID: &userID} }

// TokenActor is an API token: a script, a curl client, or the bridge as
// it was before the services merged and it needed one.
func TokenActor(tokenID int64) Actor { return Actor{Kind: ActorToken, TokenID: &tokenID} }

// PlayerActor is a logged-in player acting on their own results.
func PlayerActor(userID int64) Actor { return Actor{Kind: ActorPlayer, UserID: &userID} }

// SystemActor is the application acting on its own, such as generating the
// share slug on first startup.
func SystemActor() Actor { return Actor{Kind: ActorSystem} }

// Audit appends an entry to the log.
//
// It takes a Querier so it runs inside the same transaction as the change it
// describes. That is what makes the log trustworthy: a crash between
// the mutation and its record cannot leave one without the other.
//
// detail is optional and is stored as JSON. On an overwrite it should carry
// the previous value, which is what turns a list of events into a correction
// trail.
func Audit(ctx context.Context, q Querier, actor Actor, action, subjectType string, subjectID *int64, detail any) error {
	var encoded any
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode audit detail for %s: %w", action, err)
		}
		encoded = string(b)
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO audit_log
			(actor_kind, actor_user_id, actor_token_id, action, subject_type, subject_id, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actor.Kind, actor.UserID, actor.TokenID, action, subjectType, subjectID, encoded,
	)
	if err != nil {
		return fmt.Errorf("write audit entry %s: %w", action, err)
	}
	return nil
}
