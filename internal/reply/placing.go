package reply

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The placing test asks a model a fixed set of questions and scores how
// many it places as they should be. It runs against a real model server,
// which is the point: unit tests cover the answers, and only a model can
// say how well it reads the questions. `wordleland placing-test` runs it.
//
// The roster, the day and the asker are fixed, so every expected date and
// name is known in advance, and nothing is read from the database.
var (
	PlacingPlayers = []string{"Alma", "Bo", "Cid", "Dana", "Erik"}
	PlacingToday   = time.Date(2026, time.September, 15, 12, 0, 0, 0, time.Local)
	placingAsker   = "Alma"
)

// PlacingCase is one question and what placing it should give. Only the
// fields named in Check are compared, besides the kind, which always is:
// a question that says nothing of a span is right whatever span it gets.
type PlacingCase struct {
	Question string
	Want     Request
	Check    string
}

// PlacingCases are the group's kinds of question, mostly in Swedish, as
// the group asks them. Every kind has at least one; a test says so.
var PlacingCases = []PlacingCase{
	{"vem leder?", Request{Kind: KindLeader, Span: SpanMonth}, "span worst"},
	{"who's leading this month?", Request{Kind: KindLeader, Span: SpanMonth}, "span worst"},
	{"vem är sämst den här månaden?", Request{Kind: KindLeader, Span: SpanMonth, Worst: true}, "span worst"},
	{"vem leder den här veckan?", Request{Kind: KindLeader, Span: SpanWeek}, "span"},
	{"vem var jumbo förra veckan?", Request{Kind: KindLeader, Span: SpanLastWeek, Worst: true}, "span worst"},
	{"vem har bäst snitt de senaste 14 dagarna?", Request{Kind: KindLeader, Span: SpanDays, Days: 14}, "span days"},
	{"vem vann augusti?", Request{Kind: KindLeader, Month: "2026-08"}, "month"},
	{"hur går det för mig?", Request{Kind: KindStanding, Player: "Alma"}, "player"},
	{"ställningen", Request{Kind: KindStanding}, "player"},
	{"har Bo någon svit igång?", Request{Kind: KindStreak, Player: "Bo"}, "player"},
	{"vem har längst svit?", Request{Kind: KindStreak}, "player"},
	{"vem har inte postat idag?", Request{Kind: KindToday}, ""},
	{"vem är kvar att svara idag?", Request{Kind: KindToday}, ""},
	{"vad fick Bo igår?", Request{Kind: KindScore, Player: "Bo", Date: "2026-09-14"}, "player date"},
	{"vad fick jag på Wordle 1900?", Request{Kind: KindScore, Player: "Alma", Date: "2026-09-01"}, "player date"},
	{"vad fick alla igår?", Request{Kind: KindDay, Date: "2026-09-14"}, "date"},
	{"var dagens ord svårt?", Request{Kind: KindDay}, "date"},
	{"vem har vunnit flest månader?", Request{Kind: KindWins}, "player"},
	{"kan Bo fortfarande vinna månaden?", Request{Kind: KindCatchup, Player: "Bo"}, "player"},
	{"kan någon komma ikapp mig?", Request{Kind: KindCatchup}, ""},
	{"om jag får en 2:a imorgon, leder jag då?",
		Request{Kind: KindWhatIf, Date: "2026-09-16", Scores: []Hypothetical{{"Alma", 2}}}, "date scores"},
	{"om Bo får ett X och Cid en 3:a idag, vem leder då?",
		Request{Kind: KindWhatIf, Scores: []Hypothetical{{"Bo", 7}, {"Cid", 3}}}, "scores"},
	{"hur många 2:or har jag?", Request{Kind: KindCount, Player: "Alma", Guesses: 2}, "player guesses orbetter"},
	{"how many 1s does Bo have?", Request{Kind: KindCount, Player: "Bo", Guesses: 1}, "player guesses"},
	{"vem har flest X?", Request{Kind: KindCount, Guesses: 7}, "player guesses worst"},
	{"vem har minst antal X?", Request{Kind: KindCount, Guesses: 7, Worst: true}, "player guesses worst"},
	{"hur många 3 eller bättre har Dana?", Request{Kind: KindCount, Player: "Dana", Guesses: 3, OrBetter: true}, "player guesses orbetter"},
	{"hur står jag mot Bo?", Request{Kind: KindVersus, Player: "Bo"}, "pair"},
	{"vem är bäst av Cid och Dana?", Request{Kind: KindVersus, Player: "Cid", Other: "Dana"}, "pair"},
	{"vem vinner flest dagar?", Request{Kind: KindDayWins}, "player"},
	{"vem är i form just nu?", Request{Kind: KindForm}, "worst player"},
	{"vem är i sämst form?", Request{Kind: KindForm, Worst: true}, "worst player"},
	{"vem är stabilast?", Request{Kind: KindSteady}, "player"},
	{"vilket var det svåraste pusslet i augusti?", Request{Kind: KindPuzzles, Month: "2026-08"}, "month worst"},
	{"vilket var det lättaste ordet den här månaden?", Request{Kind: KindPuzzles, Worst: true}, "worst"},
	{"vilken veckodag är svårast?", Request{Kind: KindWeekday}, "player"},
	{"berätta om Erik", Request{Kind: KindProfile, Player: "Erik"}, "player"},
	{"tell me about Cid", Request{Kind: KindProfile, Player: "Cid"}, "player"},
	{"roasta Bo", Request{Kind: KindProfile, Player: "Bo"}, "player"},
	{"hur har jag gått månad för månad?", Request{Kind: KindHistory, Player: "Alma"}, "player"},
	{"vilka är rekorden?", Request{Kind: KindRecords}, ""},
	{"hur många spelar i gruppen?", Request{Kind: KindGroup}, ""},
	{"vem brukar posta först?", Request{Kind: KindHabits}, "player"},
	{"vad räknas som en miss?", Request{Kind: KindRules, Topic: TopicMiss}, "topic"},
	{"vad var dagens ord?", Request{Kind: KindRules, Topic: TopicData}, "topic"},
	{"vad kan du?", Request{Kind: KindHelp}, ""},
	{"tack, duktig bot!", Request{Kind: KindThanks}, ""},
	{"vad är huvudstaden i Norge?", Request{Kind: KindUnknown}, ""},
	{"vem leder, och har jag svit?",
		Request{Kind: KindLeader, Also: []Request{{Kind: KindStreak, Player: "Alma"}}}, "also"},
}

// PlacingResult is how one question went.
type PlacingResult struct {
	Case PlacingCase
	Got  Request
	// Misses are the checked fields that came out wrong, as
	// "field: got … want …"; empty when the question was placed right.
	Misses []string
	Err    error
	Took   time.Duration
}

// Placed reports whether the question was placed as it should be.
func (r PlacingResult) Placed() bool { return r.Err == nil && len(r.Misses) == 0 }

// PlacingPrompt is what the model is told besides the question, as the bot
// tells it.
func PlacingPrompt(question string) Prompt {
	return Prompt{Question: question, Asker: placingAsker, Players: PlacingPlayers, Today: PlacingToday}
}

// RunPlacing asks interp every case in turn, and hands each result to
// report as it comes, since a CPU takes seconds a question.
func RunPlacing(ctx context.Context, interp Interpreter, cases []PlacingCase, report func(PlacingResult)) []PlacingResult {
	out := make([]PlacingResult, 0, len(cases))
	for _, c := range cases {
		start := time.Now()
		got, err := interp.Interpret(ctx, PlacingPrompt(c.Question))
		r := PlacingResult{Case: c, Got: got, Err: err, Took: time.Since(start)}
		if err == nil {
			r.Misses = placingMisses(c, got)
		}
		out = append(out, r)
		report(r)
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

// placingMisses compares the kind, and the fields the case names.
func placingMisses(c PlacingCase, got Request) []string {
	want := c.Want
	var misses []string
	miss := func(field string, g, w any) {
		misses = append(misses, fmt.Sprintf("%s: got %v, want %v", field, g, w))
	}
	if got.Kind != want.Kind {
		miss("kind", got.Kind, want.Kind)
		// Nothing else means anything once the kind is wrong.
		return misses
	}
	for _, field := range strings.Fields(c.Check) {
		switch field {
		case "span":
			if got.Span != want.Span {
				miss(field, got.Span, want.Span)
			}
		case "days":
			if got.Days != want.Days {
				miss(field, got.Days, want.Days)
			}
		case "worst":
			if got.Worst != want.Worst {
				miss(field, got.Worst, want.Worst)
			}
		case "player":
			if !strings.EqualFold(got.Player, want.Player) {
				miss(field, quoted(got.Player), quoted(want.Player))
			}
		case "guesses":
			if got.Guesses != want.Guesses {
				miss(field, got.Guesses, want.Guesses)
			}
		case "orbetter":
			if got.OrBetter != want.OrBetter {
				miss(field, got.OrBetter, want.OrBetter)
			}
		case "date":
			if got.Date != want.Date {
				miss(field, quoted(got.Date), quoted(want.Date))
			}
		case "month":
			if got.Month != want.Month {
				miss(field, quoted(got.Month), quoted(want.Month))
			}
		case "topic":
			if got.Topic != want.Topic {
				miss(field, quoted(string(got.Topic)), quoted(string(want.Topic)))
			}
		case "scores":
			if g, w := scoresKey(got.Scores), scoresKey(want.Scores); g != w {
				miss(field, g, w)
			}
		case "pair":
			// Who is compared with whom, in any order, the asker standing
			// in for a side left empty — as the answer reads it.
			if g, w := pairKey(got), pairKey(want); g != w {
				miss("players", g, w)
			}
		case "also":
			if g, w := alsoKey(got), alsoKey(want); g != w {
				miss(field, g, w)
			}
		}
	}
	return misses
}

func quoted(s string) string { return `"` + s + `"` }

func scoresKey(scores []Hypothetical) string {
	var parts []string
	for _, h := range scores {
		parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(h.Player), h.Guesses))
	}
	slices.Sort(parts)
	return "[" + strings.Join(parts, ", ") + "]"
}

func pairKey(r Request) string {
	a, b := strings.ToLower(r.Player), strings.ToLower(r.Other)
	asker := strings.ToLower(placingAsker)
	if b == "" {
		a, b = asker, a
	}
	if a == "" {
		a = asker
	}
	pair := []string{a, b}
	slices.Sort(pair)
	return strings.Join(pair, " vs ")
}

// alsoKey is the further questions by kind and player, which is what makes
// them the right ones.
func alsoKey(r Request) string {
	var parts []string
	for _, a := range r.Also {
		parts = append(parts, string(a.Kind)+" "+quoted(strings.ToLower(a.Player)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
