package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEventNotFound covers an id that does not exist and one the log does
// not surface. They are not distinguished: both mean "no such row here".
var ErrEventNotFound = errors.New("no such activity event")

// Actor kinds, matching the CHECK on activity_log.
const (
	ActorAdmin  = "admin"
	ActorToken  = "token"
	ActorPlayer = "player"
	ActorSystem = "system"
)

// Activity actions. Constants rather than literals so a typo is a compile
// error and the vocabulary stays greppable as it grows.
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
	// ActionPlayerDeleted is written only by demo clear, the one path that
	// deletes a player rather than retiring them. subject_id has no foreign
	// key, so the entry survives the row it names — the activity log already
	// renders a vanished subject as "#id".
	ActionPlayerDeleted = "player.deleted"

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
// set, except for Kind == ActorSystem where neither is; the activity_log
// CHECK enforces the same rule at the database.
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

// LogActivity appends an entry to the log.
//
// It takes a Querier so it runs inside the same transaction as the change it
// describes. That is what makes the log trustworthy: a crash between
// the mutation and its record cannot leave one without the other.
//
// detail is optional and is stored as JSON. On an overwrite it should carry
// the previous value, which is what turns a list of events into a correction
// trail.
func LogActivity(ctx context.Context, q Querier, actor Actor, action, subjectType string, subjectID *int64, detail any) error {
	var encoded any
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode activity detail for %s: %w", action, err)
		}
		encoded = string(b)
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO activity_log
			(actor_kind, actor_user_id, actor_token_id, action, subject_type, subject_id, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actor.Kind, actor.UserID, actor.TokenID, action, subjectType, subjectID, encoded,
	)
	if err != nil {
		return fmt.Errorf("write activity entry %s: %w", action, err)
	}
	return nil
}

// Activity categories. The log records fine-grained actions; an admin
// reading it wants three questions answered — what was scored, who changed
// on the roster, and what happened to logins — so the actions are grouped
// into those rather than listed raw.
const (
	ActivityResults = "results"
	ActivityPlayers = "players"
	ActivityUsers   = "users"
)

// Event is one line of the activity log.
type Event struct {
	ID   int64
	At   time.Time
	Kind string

	// Action is the raw action name, which the view maps to copy.
	Action string

	ActorKind  string
	ActorEmail string
	// ActorToken is the token's label when a token made the change.
	ActorToken string

	SubjectType string
	SubjectID   *int64
	// SubjectSlug names the player a row is about, empty when the row is
	// not about one.
	SubjectSlug string

	Detail string
}

// activityKinds maps an action to the category it belongs to. An action
// missing from here is left out of the log rather than shown uncategorised:
// the page is a summary for an admin, not a database dump.
var activityKinds = map[string]string{
	ActionResultCreated: ActivityResults,
	ActionResultUpdated: ActivityResults,
	ActionResultDeleted: ActivityResults,

	ActionPlayerCreated:     ActivityPlayers,
	ActionPlayerUpdated:     ActivityPlayers,
	ActionPlayerRetired:     ActivityPlayers,
	ActionPlayerReactivated: ActivityPlayers,
	ActionPlayerLinked:      ActivityPlayers,
	ActionPlayerUnlinked:    ActivityPlayers,
	ActionIdentityAdded:     ActivityPlayers,

	ActionUserCreated:         ActivityUsers,
	ActionUserDisabled:        ActivityUsers,
	ActionUserEnabled:         ActivityUsers,
	ActionUserPasswordReset:   ActivityUsers,
	ActionUser2FAReset:        ActivityUsers,
	ActionUser2FAEnrolled:     ActivityUsers,
	ActionRecoveryCodesIssued: ActivityUsers,
	ActionRecoveryCodeUsed:    ActivityUsers,
	ActionUserEmailPending:    ActivityUsers,
	ActionUserEmailChanged:    ActivityUsers,
	ActionInvitationSent:      ActivityUsers,
	ActionInvitationAccepted:  ActivityUsers,
	ActionTokenCreated:        ActivityUsers,
	ActionTokenRevoked:        ActivityUsers,
}

// ActivityKind reports which category an action belongs to, or "".
func ActivityKind(action string) string { return activityKinds[action] }

// ListActivity reads the most recent events, optionally of one category.
//
// The subject's name is joined in rather than looked up per row: a log of a
// hundred lines would otherwise be a hundred queries, and the name is what
// activitySelect is the projection both reads share, so a detail page and
// the list it was opened from can never disagree about a row.
const activitySelect = `
		SELECT a.id, a.at, a.action, a.actor_kind,
		       COALESCE(u.email, ''), a.subject_type, a.subject_id,
		       COALESCE(p.slug, su.email, ''), COALESCE(a.detail, ''),
		       COALESCE(tk.label, '')
		FROM activity_log a
		LEFT JOIN users u   ON u.id = a.actor_user_id
		-- A token write is done by whatever the operator named that token,
		-- which is the only honest answer to "who changed this". Tokens are
		-- never deleted once they have acted (activity_log holds them under
		-- RESTRICT), so the label survives revocation.
		LEFT JOIN api_tokens tk ON tk.id = a.actor_token_id
		-- A result's subject_id is the player it belongs to, not a result
		-- id, so one join names the player for both kinds. The slug rather
		-- than the display name: it is the identifier an admin acts on, it
		-- is unique where a display name is not, and it survives a rename.
		LEFT JOIN players p ON a.subject_type IN ('player', 'result')
		                   AND p.id = a.subject_id
		-- A row about an account names it too, or "Two-factor set up" ends
		-- up reading as "· #1".
		LEFT JOIN users su  ON a.subject_type = 'user'
		                   AND su.id = a.subject_id`

// makes a line readable at all.
func ListActivity(ctx context.Context, q Querier, kind string, limit int) ([]Event, int, error) {
	var wanted []string
	for action, k := range activityKinds {
		if kind == "" || k == kind {
			wanted = append(wanted, action)
		}
	}
	if len(wanted) == 0 {
		return nil, 0, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(wanted)), ",")
	args := make([]any, 0, len(wanted)+1)
	for _, a := range wanted {
		args = append(args, a)
	}

	var total int
	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM activity_log WHERE action IN (`+placeholders+`)`, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count activity: %w", err)
	}

	rows, err := q.QueryContext(ctx, activitySelect+`
		WHERE a.action IN (`+placeholders+`)
		ORDER BY a.id DESC
		LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.Action, &e.ActorKind, &e.ActorEmail,
			&e.SubjectType, &e.SubjectID, &e.SubjectSlug, &e.Detail, &e.ActorToken); err != nil {
			return nil, 0, fmt.Errorf("scan activity: %w", err)
		}
		e.Kind = activityKinds[e.Action]
		events = append(events, e)
	}
	return events, total, rows.Err()
}

// ActivityEvent reads one event by id, for the detail behind a row.
func ActivityEvent(ctx context.Context, q Querier, id int64) (Event, error) {
	var e Event
	err := q.QueryRowContext(ctx, activitySelect+`
		WHERE a.id = ?`, id).Scan(&e.ID, &e.At, &e.Action, &e.ActorKind, &e.ActorEmail,
		&e.SubjectType, &e.SubjectID, &e.SubjectSlug, &e.Detail, &e.ActorToken)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrEventNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("read activity event: %w", err)
	}
	e.Kind = activityKinds[e.Action]
	if e.Kind == "" {
		// An action the log does not surface. Treated as absent rather
		// than shown, so the detail page cannot become a way to read rows
		// the list deliberately filters out.
		return Event{}, ErrEventNotFound
	}
	return e, nil
}
