package web

import (
	"net/http"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/version"
)

// staleAfter is how long the board may go without a result before the
// admin area says so.
//
// The group posts daily, so a day and a half is already unusual without
// being alarming on a quiet weekend. This is a prompt to look, not a fault:
// the liveness probe is deliberately not involved.
const staleAfter = 36 * time.Hour

// diagnosticRow is one line of the page: a label, a value, and how worried
// to look about it.
type diagnosticRow struct {
	Label string
	Value string
	Code  bool
	// Tone is "", "warn" or "bad", for styling only.
	Tone string
	Hint string
}

type diagnosticsPage struct {
	chrome

	// Status is the bridge in one line, at the top; Stats the four figures
	// under it; Rows the bridge's details, in the card below them.
	Status diagStatus
	Stats  []diagStat
	Rows   []diagnosticRow

	// Configured is false when no Signal bridge is set up, which is a
	// deployment choice rather than a problem.
	Configured bool
	Warning    string
}

// diagStatus is the bridge in a sentence, and a dot for how worried to be.
type diagStatus struct {
	// Tone is "ok", "warn", "bad" or "off".
	Tone  string
	Title string
	Sub   string
}

// diagStat is one of the four figures: a glyph, what it is, the figure,
// a line under it, and a way to act on it when there is one.
type diagStat struct {
	Icon, Label, Value, Sub string
	Tone                    string
	Href, Link              string
}

// handleAdminDiagnostics reports whether results are still arriving.
//
// Freshness first, connection state last. A bridge pointed at the wrong
// group is connected, answering and delivering nothing, and that is the
// failure that quietly costs a season of scores — a connection indicator is
// green throughout it.
func (s *Server) handleAdminDiagnostics(w http.ResponseWriter, r *http.Request) {
	page := diagnosticsPage{chrome: s.adminChrome(w, r, "diagnostics")}
	t := page.T
	now := time.Now()

	fresh, err := store.ReadFreshness(r.Context(), s.db)
	if err != nil {
		s.logger.Error("read freshness", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page.Configured = s.bridge != nil
	page.Status = s.bridgeStatus(t, now)
	page.Stats = s.diagStats(r, t, fresh, now)
	if s.bridge != nil {
		// The first two are the status line's; the rest are details.
		page.Rows = s.bridgeRows(t, s.bridge, now)[2:]
	}

	page.Warning = s.diagnosticsWarning(t, fresh, now)

	s.render(w, r, http.StatusOK, "admin_diagnostics.html", page)
}

func (s *Server) bridgeRows(t translator, b Bridge, now time.Time) []diagnosticRow {
	alive, why := b.Alive()
	st := b.Status()

	running := diagnosticRow{Label: t.T("diag.bridge"), Value: t.T("diag.running")}
	if !alive {
		running.Value = why
		running.Tone = "bad"
	}

	connection := diagnosticRow{Label: t.T("diag.connection")}
	switch {
	case st.Connected:
		connection.Value = t.T("diag.connected")
	default:
		connection.Value = t.T("diag.reconnecting")
		connection.Tone = "warn"
	}
	if !st.Since.IsZero() {
		connection.Hint = sinceText(t, st.Since, now)
	}

	rows := []diagnosticRow{running, connection}

	// What the bridge is watching, and whether signal-cli agrees it can
	// work. Both of these were invisible when a well-formed but wrong
	// account produced a connection that received nothing for eight hours.
	// Both shown in full and unmasked. This page is behind requireAdmin and
	// the reader is the person who configured these, so hiding a value from
	// them only makes it harder to compare against the environment file it
	// came from — which is the entire job of these two rows. The account was
	// masked once; a missing leading + is exactly what that hid.
	//
	// The group id is an identifier, not a credential. Signal derives it
	// from the group master key, and it is the master key — the thing inside
	// an invite link — that grants access. Knowing the id joins nobody to
	// anything.
	if st.Account != "" {
		rows = append(rows, diagnosticRow{
			Label: t.T("diag.account"),
			Value: st.Account,
			Code:  true,
		})
	}
	if st.Group != "" {
		group := diagnosticRow{Label: t.T("diag.group"), Value: st.Group, Code: true}
		// The name is the proof. An id that matches says two strings are
		// equal; a name says signal-cli can see the group and this is what
		// it is called, which is the question an admin actually has.
		if st.Verification.GroupName != "" {
			group.Hint = t.T("diag.groupHint", st.Verification.GroupName)
		}
		rows = append(rows, group)
	}

	// "Confirmed by signal-cli" was true and said almost nothing: it named
	// the authority without naming the claim, so a reader had to take the
	// word of a row that could not be checked. Both facts are cheap to
	// state, and they are the two ways this can be wrong.
	//
	// The time it was last checked matters as much. An hourly re-check is
	// only reassuring if a reader can see it happening, and a verdict from
	// nine hours ago describes a configuration nobody has asked about since.
	switch {
	case st.Verification.OK():
		confirmed := diagnosticRow{
			Label: t.T("diag.verified"),
			Value: t.T("diag.verified.ok"),
		}
		if !st.Verification.At.IsZero() {
			confirmed.Hint = t.T("diag.verifiedAt", sinceText(t, st.Verification.At, now))
		}
		rows = append(rows, confirmed)
	case st.Verification.Done:
		rows = append(rows, diagnosticRow{
			Label: t.T("diag.verified"),
			Value: t.T("diag.verified.bad"),
			Tone:  "bad",
			Hint:  st.Verification.Problem,
		})
	default:
		// Not yet checked is ordinary at startup, and stops being ordinary
		// if it persists — so it is shown rather than hidden.
		rows = append(rows, diagnosticRow{
			Label: t.T("diag.verified"),
			Value: t.T("diag.verified.pending"),
			Tone:  "warn",
		})
	}

	// The label invites two wrong readings, and the second one cost eight
	// hours: that this counts results, and that "never" means never at all.
	// It counts any frame the subscription delivers — a receipt, a typing
	// indicator, a reaction — which is what makes it evidence the
	// subscription works rather than evidence the group is playing. And it
	// is held in memory, so it starts empty on every restart.
	//
	// Hence two hints rather than one: a reader looking at "never" is
	// asking a different question from one looking at a duration.
	seen := diagnosticRow{
		Label: t.T("diag.lastMessage"),
		Value: t.T("diag.never"),
		Hint:  t.T("diag.lastMessageNeverHint"),
	}
	if !st.LastMessage.IsZero() {
		seen.Value = sinceText(t, st.LastMessage, now)
		seen.Hint = t.T("diag.lastMessageHint")
	}
	rows = append(rows, seen)

	// Dropped results are lost outright: nothing will redeliver them.
	if st.Dropped > 0 {
		rows = append(rows, diagnosticRow{
			Label: t.T("diag.dropped"),
			Value: t.TN("diag.droppedCount", st.Dropped),
			Tone:  "bad",
			Hint:  t.T("diag.droppedHint"),
		})
	}
	return rows
}

// diagnosticsWarning is the line the rest of the admin area shows, so a
// stalled bridge finds the reader rather than waiting to be looked for.
// adminWarning is a problem and the screen that does something about it. The
// destination follows the message: held results are claimed on Pending
// results, and everything else — a bridge that is down, a board that has gone
// quiet — is read in full on Diagnostics.
type adminWarning struct {
	Text  string
	Href  string
	Label string
}

// adminWarningFor pairs the warning with where it leads.
// adminWarningFor is what the admin area raises on the way in.
//
// It is deliberately not diagnosticsWarning: held results are the one thing
// that page reports and this band does not. A sender nobody has claimed yet
// is the ordinary state of a new member's first week rather than a fault,
// the count is on the Pending results tab where it is actually worked
// through, and saying it again above every roster made it furniture. What
// is left is a bridge that is down and a board that has gone quiet — both
// genuinely wrong, and neither visible anywhere else until somebody opens
// Diagnostics.
func (s *Server) adminWarningFor(t translator, f store.Freshness, now time.Time) adminWarning {
	toDiagnostics := func(text string) adminWarning {
		return adminWarning{Text: text, Href: "/admin/diagnostics", Label: t.T("diag.title")}
	}
	if s.bridge != nil {
		if alive, why := s.bridge.Alive(); !alive {
			return toDiagnostics(why)
		}
	}
	if !f.LastResultAt.IsZero() && now.Sub(f.LastResultAt) > staleAfter {
		return toDiagnostics(t.T("diag.warn.stale", sinceText(t, f.LastResultAt, now)))
	}
	return adminWarning{}
}

func (s *Server) diagnosticsWarning(t translator, f store.Freshness, now time.Time) string {
	if s.bridge != nil {
		if alive, why := s.bridge.Alive(); !alive {
			return why
		}
	}
	if f.PendingResults > 0 {
		return t.TN("diag.warn.pending", f.PendingResults)
	}
	if !f.LastResultAt.IsZero() && now.Sub(f.LastResultAt) > staleAfter {
		return t.T("diag.warn.stale", sinceText(t, f.LastResultAt, now))
	}
	return ""
}

var _ = bridge.Status{}

// bridgeStatus says in one line whether results can arrive: no bridge, a
// bridge that is down, one reconnecting, or one running and connected.
func (s *Server) bridgeStatus(t translator, now time.Time) diagStatus {
	if s.bridge == nil {
		return diagStatus{Tone: "off", Title: t.T("diag.status.none"), Sub: t.T("diag.noBridge")}
	}
	alive, why := s.bridge.Alive()
	st := s.bridge.Status()
	switch {
	case !alive:
		return diagStatus{Tone: "bad", Title: t.T("diag.status.down"), Sub: why}
	case !st.Connected:
		return diagStatus{Tone: "warn", Title: t.T("diag.status.reconnecting"), Sub: t.T("diag.status.reconnectingSub")}
	}
	status := diagStatus{Tone: "ok", Title: t.T("diag.status.ok"), Sub: t.T("diag.status.okSub")}
	if name := st.Verification.GroupName; name != "" {
		status.Sub = t.T("diag.status.watching", name) + " " + status.Sub
	}
	if !st.Since.IsZero() {
		status.Sub += " " + t.T("diag.status.since", i18n.ClockTime(st.Since))
	}
	return status
}

// diagStats is the four figures: when a result last arrived, the newest
// puzzle and how far through it the group is, what is held for senders
// nobody has claimed, and which build is running.
func (s *Server) diagStats(r *http.Request, t translator, f store.Freshness, now time.Time) []diagStat {
	last := diagStat{Icon: "schedule", Label: t.T("diag.lastResult"), Value: t.T("diag.stat.never"), Sub: t.T("diag.stat.noneYet"), Tone: "warn"}
	if !f.LastResultAt.IsZero() {
		last.Value, last.Sub, last.Tone = f.LastResultAt.Local().Format("15:04"), sinceText(t, f.LastResultAt, now), ""
		if now.Sub(f.LastResultAt) > staleAfter {
			last.Tone, last.Sub = "warn", t.T("diag.staleHint")
		}
	}

	puzzle := diagStat{Icon: "tag", Label: t.T("diag.latestPuzzle"), Value: t.T("diag.none")}
	if f.LatestPuzzle > 0 {
		puzzle.Value = t.T("player.puzzle", t.Puzzle(f.LatestPuzzle))
		if _, players, results, _, err := s.boardData(r); err == nil {
			day := stats.ComputeToday(players, results, f.LatestPuzzle)
			puzzle.Sub = t.T("diag.stat.soFar", day.FiledCount(), day.Expected())
		}
	}

	held := diagStat{Icon: "inbox", Label: t.T("diag.pending"), Value: t.Integer(f.PendingResults), Sub: t.T("diag.stat.heldSub")}
	if f.PendingResults > 0 {
		held.Tone, held.Href, held.Link = "warn", "/admin/pending", t.T("diag.stat.review")
	}

	build := diagStat{Icon: "deployed_code", Label: t.T("diag.version"), Value: version.Short(), Sub: version.String()}
	if !version.Set() {
		build.Sub = t.T("diag.versionUnstamped")
	}
	return []diagStat{last, puzzle, held, build}
}
