package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
)

// activityLimit bounds the page. The log grows with every write, and an
// admin reading it wants the recent end.
const activityLimit = 120

type activityRow struct {
	// Href opens the detail behind the row, on a page of its own.
	Href string

	Kind  string
	Tag   string
	Text  string
	Actor string
	When  string

	// As the design draws a row: a glyph for its kind, what happened and
	// to whom on two lines, who did it with a glyph for what they are, and
	// the clock time. Opened, it shows the stored detail as it was written.
	Icon      string
	Title     string
	Detail    string
	ActorIcon string
	Clock     string
	JSON      string

	// Grid is the squares a result was filed with, drawn beside the
	// scoreline when opened; "" when the entry carries none.
	Grid string
}

// activityDay is the rows of one day, under its name.
type activityDay struct {
	Label string
	Rows  []activityRow
}

type activityPage struct {
	chrome

	Filters []chromeOpt
	Rows    []activityRow

	Shown int
	Count int

	// Summary names the slice being shown, beside the filters.
	Summary string

	// Days is Rows grouped by the day they happened, newest first.
	Days []activityDay
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
		opt := chromeOpt{Code: activityFilterIcons[f.code], Label: page.T.T(f.key), Href: href, On: f.code == kind}
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

	now := time.Now()
	for _, e := range events {
		row := s.activityRowFor(e, page.T)
		page.Rows = append(page.Rows, row)
		label := activityDayLabel(page.T, e.At, now)
		if n := len(page.Days); n == 0 || page.Days[n-1].Label != label {
			page.Days = append(page.Days, activityDay{Label: label})
		}
		page.Days[len(page.Days)-1].Rows = append(page.Days[len(page.Days)-1].Rows, row)
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "admin_activity.html", page)
}

// activityFilterIcons is each filter's glyph, as the design has them.
var activityFilterIcons = map[string]string{
	"":                    "list",
	store.ActivityResults: "scoreboard",
	store.ActivityPlayers: "person",
	store.ActivityUsers:   "key",
}

// activityDayLabel names the day a row happened: today and yesterday by
// those words, with the date after them, and any other day by its date.
func activityDayLabel(t translator, at, now time.Time) string {
	at, now = at.Local(), now.Local()
	date := t.T("weekday.short."+strconv.Itoa(int(at.Weekday()))) + " " + dayMonth(t, at)
	y1, m1, d1 := at.Date()
	y2, m2, d2 := now.Date()
	switch days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours() / 24); days {
	case 0:
		return t.T("activity.day.today", date)
	case 1:
		return t.T("activity.day.yesterday", date)
	}
	return date
}

// activityRowFor turns one event into a line of copy.
//
// The detail column is JSON written at the time of the change, so it is
// read defensively: an entry from an older version that no longer parses
// still shows its action and its time rather than breaking the page.
func (s *Server) activityRowFor(e store.Event, t translator) activityRow {
	row := activityRow{
		Kind:  e.Kind,
		Tag:   t.T("activity.tag." + e.Kind),
		When:  absoluteTime(e.At),
		Text:  t.T("activity.action." + e.Action),
		Href:  "/admin/activity/" + strconv.FormatInt(e.ID, 10),
		Icon:  activityFilterIcons[e.Kind],
		Clock: e.At.Local().Format("15:04"),
		JSON:  indentJSON(e.Detail),
	}
	row.Title = row.Text

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
	row.ActorIcon = "person"
	if e.ActorKind != "admin" && e.ActorKind != "player" {
		row.ActorIcon = "chat"
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
		row.Detail = subject
	}

	// A result carries which puzzle it was, which is the one thing that
	// makes two otherwise identical lines tell apart, and the score itself.
	if e.Kind == store.ActivityResults {
		if puzzle, ok := detailNumber(detail, "puzzle_no"); ok {
			row.Text += " · " + t.T("player.puzzle", t.Puzzle(puzzle))
			row.Detail += " · " + t.T("player.puzzle", t.Puzzle(puzzle))
		}
		if line := scorelineFrom(detail); line != "" {
			row.Detail += " · " + line
		}
		row.Grid = detailString(detail, "grid")
	}
	return row
}

func detailString(detail map[string]any, key string) string {
	if v, ok := detail[key].(string); ok {
		return v
	}
	return ""
}

// detailNumber reads a number out of the activity detail as an int, because
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

// sinceText renders when something happened relative to the local calendar.
func sinceText(t translator, when time.Time, now time.Time) string {
	localWhen, localNow := when.Local(), now.Local()
	whenDay := time.Date(localWhen.Year(), localWhen.Month(), localWhen.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	days := int(nowDay.Sub(whenDay).Hours() / 24)
	clock := i18n.ClockTime(when)
	switch {
	case days < -1:
		return t.TN("activity.inDays", -days)
	case days == -1:
		return t.T("activity.tomorrowAt", clock)
	case days == 0:
		// The clock time, not just "today". On a page whose question is
		// whether results are still arriving, a whole day is the wrong
		// resolution: "today" is true at one minute past midnight and at
		// eleven at night, and only one of those is reassuring.
		return t.T("activity.todayAt", clock)
	case days == 1:
		return t.T("activity.yesterdayAt", clock)
	default:
		return t.TN("activity.daysAgo", days)
	}
}
