package reply

import (
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
)

// toneLine is the line an answer opens with when the question was said
// with feeling, or "". A brag or a worry about the race is met according
// to how the race stands — "Bold claim." only for somebody who is not on
// top — so the line is as true as the figures under it. A tease about
// another player gets a nudge, whatever the figures say.
func toneLine(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	switch req.Tone {
	case ToneTease:
		if req.Player == "" || (asker != nil && strings.EqualFold(req.Player, asker.Name)) {
			return ""
		}
		return t.T("reply.tone.tease")
	case ToneBoast, ToneWorried:
		top, known := onTop(t, req, asker, players, results, now)
		switch {
		case !known:
			return ""
		case top:
			return t.T("reply.tone." + string(req.Tone) + ".top")
		default:
			return t.T("reply.tone." + string(req.Tone) + ".behind")
		}
	}
	return ""
}

// onTop says whether the player a race question is about — the one named,
// or the asker — leads or shares the lead over the span it asks about.
// known is false when there is nothing to judge: a question that is not
// about the race, about the bottom of the table, or about somebody with
// no place in it.
func onTop(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) (top, known bool) {

	switch req.Kind {
	case KindLeader, KindStanding, KindCatchup:
	default:
		return false, false
	}
	if req.Worst {
		return false, false
	}
	var p store.Player
	switch {
	case req.Player != "":
		found, ok := findPlayer(req.Player, players)
		if !ok {
			return false, false
		}
		p = found
	case asker != nil:
		p = *asker
	default:
		return false, false
	}
	if req.Kind == KindCatchup {
		// The race catch-up is about is the month's, whatever span the
		// model also wrote.
		req.Span, req.Days = SpanMonth, 0
	}
	_, m := standingOver(t, req, players, results, now)
	ranked := false
	for _, mp := range m.Ranked {
		ranked = ranked || mp.ID == p.ID
	}
	if !ranked {
		return false, false
	}
	for _, w := range m.Winners {
		if w.ID == p.ID {
			return true, true
		}
	}
	return false, true
}
