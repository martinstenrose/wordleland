package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/store"
)

// activityLimit bounds the page. The log grows with every write, and an
// admin reading it wants the recent end.
const activityLimit = 120

type activityRow struct {
	// Href opens the detail behind the row.
	Href string

	Kind  string
	Tag   string
	Text  string
	Actor string
	When  string
}

type activityPage struct {
	chrome

	Filters []chromeOpt
	Rows    []activityRow

	Shown int
	Count int

	// Summary names the slice being shown, beside the filters.
	Summary string
}

// handleAdminActivity lists what admins have done and what has been logged.
func (s *Server) handleAdminActivity(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	switch kind {
	case store.ActivityResults, store.ActivityPlayers, store.ActivityUsers:
	default:
		kind = ""
	}

	events, total, err := store.ListActivity(r.Context(), s.db, kind, activityLimit)
	if err != nil {
		s.logger.Error("list activity", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page := activityPage{
		chrome: s.adminChrome(w, r, "activity"),

		Shown: len(events),
		Count: total,
	}

	for _, f := range []struct{ code, key string }{
		{"", "activity.filter.all"},
		{store.ActivityResults, "activity.filter.results"},
		{store.ActivityPlayers, "activity.filter.players"},
		{store.ActivityUsers, "activity.filter.users"},
	} {
		href := "/admin/activity"
		if f.code != "" {
			href += "?kind=" + f.code
		}
		opt := chromeOpt{Code: f.code, Label: page.T.T(f.key), Href: href, On: f.code == kind}
		if opt.On {
			// Names the slice on show beside the filters, so the count
			// below is read against the right population.
			page.Summary = opt.Label
			if f.code == "" {
				page.Summary = page.T.T("activity.summaryAll")
			}
		}
		page.Filters = append(page.Filters, opt)
	}

	for _, e := range events {
		page.Rows = append(page.Rows, s.activityRowFor(e, page.T))
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "admin_activity.html", page)
}

// activityRowFor turns one event into a line of copy.
//
// The detail column is JSON written at the time of the change, so it is
// read defensively: an entry from an older version that no longer parses
// still shows its action and its time rather than breaking the page.
func (s *Server) activityRowFor(e store.Event, t translator) activityRow {
	row := activityRow{
		Kind: e.Kind,
		Tag:  t.T("activity.tag." + e.Kind),
		When: absoluteTime(e.At),
		Text: t.T("activity.action." + e.Action),
		Href: "/admin/activity/" + strconv.FormatInt(e.ID, 10),
	}

	// Read before the actor is named, because a system row is only as
	// specific as its detail: see below.
	var detail map[string]any
	if e.Detail != "" {
		_ = json.Unmarshal([]byte(e.Detail), &detail)
	}

	switch e.ActorKind {
	case "token":
		// The column asks who changed this, so it answers with the token's
		// own label rather than describing how the score was attributed.
		// A token with no label left is still not a person, so the generic
		// name stands in.
		row.Actor = e.ActorToken
		if row.Actor == "" {
			row.Actor = t.T("activity.actor.token")
		}
	case "system":
		// "system" covers everything the app does unprompted, from minting
		// the share slug to filing a result off Signal, and on a log that
		// is mostly the latter it answers the column's question with
		// nothing. The source is already recorded, so use it.
		//
		// Only the bridge is named. A future source gets its own case here
		// rather than a generic "via %s", which would put a raw stored
		// value in front of a reader.
		row.Actor = t.T("activity.actor.system")
		if detailString(detail, "via") == bridge.SourceSignal {
			row.Actor = t.T("activity.actor.bridge")
		}
	default:
		row.Actor = e.ActorEmail
	}

	// The slug first, then the address for a user row. A bare id is the
	// last resort and means the subject is gone — a player deleted outright
	// rather than retired, which the app itself never does.
	subject := e.SubjectSlug
	if subject == "" {
		subject = detailString(detail, "email")
	}
	if subject == "" && e.SubjectID != nil {
		subject = "#" + strconv.FormatInt(*e.SubjectID, 10)
	}
	if subject != "" {
		row.Text = t.T("activity.line", row.Text, subject)
	}

	// A result carries which puzzle it was, which is the one thing that
	// makes two otherwise identical lines tell apart.
	if e.Kind == store.ActivityResults {
		if puzzle, ok := detailNumber(detail, "puzzle_no"); ok {
			row.Text += " · " + t.T("player.puzzle", puzzle)
		}
	}
	return row
}

func detailString(detail map[string]any, key string) string {
	if v, ok := detail[key].(string); ok {
		return v
	}
	return ""
}

// detailNumber reads a number out of the audit detail as an int, because
// that is what the copy's %d verb needs. JSON has no integers, so a value
// round-tripped through the log arrives as a float64; one written as a
// string is parsed rather than passed through, which is what produced
// "#%!d(string=1895)".
func detailNumber(detail map[string]any, key string) (int, bool) {
	switch v := detail[key].(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// maskIdentity hides most of an external identifier.
//
// The pending list shows who has not been claimed yet, and those are phone
// numbers and chat handles belonging to people who may never join. Enough
// is shown to tell two senders apart and no more.
func maskIdentity(id string) string {
	runes := []rune(strings.TrimSpace(id))
	switch {
	case len(runes) == 0:
		return "—"
	case len(runes) <= 4:
		return strings.Repeat("•", len(runes))
	}
	return "••••" + string(runes[len(runes)-4:])
}

// sinceText renders how long ago something happened, coarsely.
func sinceText(t translator, when time.Time, now time.Time) string {
	days := int(now.Sub(when).Hours() / 24)
	switch {
	case days <= 0:
		// The clock time, not just "today". On a page whose question is
		// whether results are still arriving, a whole day is the wrong
		// resolution: "today" is true at one minute past midnight and at
		// eleven at night, and only one of those is reassuring.
		return t.T("activity.todayAt", when.Local().Format("15:04:05 -07:00"))
	case days == 1:
		return t.T("activity.yesterdayAt", when.Local().Format("15:04:05 -07:00"))
	default:
		return t.TN("activity.daysAgo", days)
	}
}
