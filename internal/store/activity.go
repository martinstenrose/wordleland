package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEventNotFound covers an id that does not exist and one the log does
// not surface. They are not distinguished: both mean "no such row here".
var ErrEventNotFound = errors.New("no such activity event")

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
		FROM audit_log a
		LEFT JOIN users u   ON u.id = a.actor_user_id
		-- A token write is done by whatever the operator named that token,
		-- which is the only honest answer to "who changed this". Tokens are
		-- never deleted once they have acted (audit_log holds them under
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
		`SELECT count(*) FROM audit_log WHERE action IN (`+placeholders+`)`, args...,
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
