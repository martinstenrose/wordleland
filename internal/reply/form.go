package reply

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// boardPlayer finds a player on the board, ranked or not.
func boardPlayer(b stats.Board, id int64) (stats.Player, bool) {
	for _, group := range [][]stats.Player{b.Ranked, b.Unranked} {
		for _, p := range group {
			if p.ID == id {
				return p, true
			}
		}
	}
	return stats.Player{}, false
}

// best is the ranked players with a figure, ordered by it, lowest first;
// ties in name order.
func best(b stats.Board, figure func(stats.Player) *float64) []stats.Player {
	var out []stats.Player
	for _, p := range b.Ranked {
		if figure(p) != nil {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := *figure(out[i]), *figure(out[j])
		if fi != fj {
			return fi < fj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// tied names everyone level with the first of ordered — or, from the
// end, the last — on figure as it is shown, to the hundredth.
func tied(t i18n.Translator, ordered []stats.Player, figure func(stats.Player) *float64, fromEnd bool) string {
	if fromEnd {
		ordered = slices.Clone(ordered)
		slices.Reverse(ordered)
	}
	shown := func(p stats.Player) string { return t.Decimal(*figure(p), 2) }
	var names []string
	for _, p := range ordered {
		if shown(p) != shown(ordered[0]) {
			break
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return joinNames(t, names)
}

// form is who is playing best right now, over the board's form window, and
// who has moved most against their own average; with Worst, the other end.
// One player named: their form against their average.
func form(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	board := stats.Compute(players, results, stats.DefaultOptions(now))
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		bp, _ := boardPlayer(board, p.ID)
		if bp.Form == nil || bp.Average == nil {
			return t.T("reply.form.player.few", p.Name, stats.MinGames, stats.FormWindow)
		}
		key := "reply.form.player.steady"
		switch {
		case *bp.Delta <= -stats.Significance:
			key = "reply.form.player.up"
		case *bp.Delta >= stats.Significance:
			key = "reply.form.player.down"
		}
		return t.T(key, p.Name, t.Decimal(*bp.Form, 2), stats.FormWindow, t.Decimal(*bp.Average, 2))
	}

	byForm := best(board, func(p stats.Player) *float64 { return p.Form })
	byDelta := best(board, func(p stats.Player) *float64 { return p.Delta })
	if len(byForm) == 0 {
		return t.T("reply.form.none", stats.FormWindow)
	}
	if req.Worst {
		formOf := func(p stats.Player) *float64 { return p.Form }
		cold := byForm[len(byForm)-1]
		lines := []string{"🥶 " + t.T("reply.form.worst", tied(t, byForm, formOf, true), t.Decimal(*cold.Form, 2), stats.FormWindow)}
		if down := byDelta[len(byDelta)-1]; *down.Delta >= stats.Significance {
			lines = append(lines, t.T("reply.form.falling",
				tied(t, byDelta, func(p stats.Player) *float64 { return p.Delta }, true), t.Decimal(*down.Delta, 2)))
		}
		return strings.Join(lines, "\n")
	}
	hot := byForm[0]
	lines := []string{"🔥 " + t.T("reply.form.best",
		tied(t, byForm, func(p stats.Player) *float64 { return p.Form }, false), t.Decimal(*hot.Form, 2), stats.FormWindow)}
	if up := byDelta[0]; *up.Delta <= -stats.Significance {
		lines = append(lines, t.T("reply.form.rising",
			tied(t, byDelta, func(p stats.Player) *float64 { return p.Delta }, false), t.Decimal(-*up.Delta, 2)))
	}
	return strings.Join(lines, "\n")
}

// steady is who scores most alike from day to day — the smallest spread —
// and who is least predictable. One player named: their spread and where
// it places them.
func steady(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	board := stats.Compute(players, results, stats.DefaultOptions(now))
	bySpread := best(board, func(p stats.Player) *float64 { return p.Spread })
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		for i, bp := range bySpread {
			if bp.ID == p.ID {
				return "🎯 " + t.T("reply.steady.player", p.Name, t.Decimal(*bp.Spread, 2), i+1, len(bySpread))
			}
		}
		return t.T("reply.steady.player.none", p.Name)
	}
	if len(bySpread) < 2 {
		return t.T("reply.steady.none")
	}
	spreadOf := func(p stats.Player) *float64 { return p.Spread }
	calm, wild := bySpread[0], bySpread[len(bySpread)-1]
	if t.Decimal(*calm.Spread, 2) == t.Decimal(*wild.Spread, 2) {
		return t.T("reply.steady.level", t.Decimal(*calm.Spread, 2))
	}
	return "🎯 " + t.T("reply.steady", tied(t, bySpread, spreadOf, false), t.Decimal(*calm.Spread, 2),
		tied(t, bySpread, spreadOf, true), t.Decimal(*wild.Spread, 2))
}
