package web

import (
	"net/http"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/store"
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
	// Tone is "", "warn" or "bad", for styling only.
	Tone string
	Hint string
}

type diagnosticsPage struct {
	chrome

	Rows []diagnosticRow

	// Configured is false when no Signal bridge is set up, which is a
	// deployment choice rather than a problem.
	Configured bool
	Warning    string
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

	page.Rows = append(page.Rows, s.freshnessRows(t, fresh, now)...)
	page.Configured = s.bridge != nil
	if s.bridge != nil {
		page.Rows = append(page.Rows, s.bridgeRows(t, s.bridge, now)...)
	}
	page.Warning = s.diagnosticsWarning(t, fresh, now)

	s.render(w, r, http.StatusOK, "admin_diagnostics.html", page)
}

func (s *Server) freshnessRows(t translator, f store.Freshness, now time.Time) []diagnosticRow {
	last := diagnosticRow{Label: t.T("diag.lastResult"), Value: t.T("diag.never"), Tone: "warn"}
	if !f.LastResultAt.IsZero() {
		last.Value = sinceText(t, f.LastResultAt, now)
		last.Tone = ""
		if now.Sub(f.LastResultAt) > staleAfter {
			last.Tone = "warn"
			last.Hint = t.T("diag.staleHint")
		}
	}

	rows := []diagnosticRow{last}

	puzzle := diagnosticRow{Label: t.T("diag.latestPuzzle"), Value: t.T("diag.none")}
	if f.LatestPuzzle > 0 {
		puzzle.Value = t.T("player.puzzle", f.LatestPuzzle)
	}
	rows = append(rows, puzzle)

	// Held results are the quiet failure: everything works and nothing
	// reaches the board, because nobody has claimed the sender.
	if f.PendingResults > 0 {
		rows = append(rows, diagnosticRow{
			Label: t.T("diag.pending"),
			Value: t.TN("diag.pendingCount", f.PendingResults),
			Tone:  "warn",
			Hint:  t.T("diag.pendingHint"),
		})
	}
	return rows
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

	seen := diagnosticRow{Label: t.T("diag.lastMessage"), Value: t.T("diag.never")}
	if !st.LastMessage.IsZero() {
		seen.Value = sinceText(t, st.LastMessage, now)
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
