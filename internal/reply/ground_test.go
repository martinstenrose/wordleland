package reply

import (
	"reflect"
	"testing"
	"time"
)

// Who a question is about is settled by its own words where they say: the
// asker only when somebody said "I", the one player named when the model
// wrote somebody else, two names as the two compared.
func TestWhoAQuestionIsAboutIsHeldToItsWords(t *testing.T) {
	t.Parallel()
	players := []string{"Alma", "Bo", "Cid Larsson", "Dana", "Bob", "Dana"}
	tests := []struct {
		question string
		read     Request
		want     Request
	}{
		// The asker filled in for a question that asks who.
		{"vem har längst svit?", Request{Kind: KindStreak, Player: Asker}, Request{Kind: KindStreak}},
		{"vem är i form?", Request{Kind: KindForm, Player: "Alma"}, Request{Kind: KindForm}},
		{"vem leder i september?", Request{Kind: KindStanding, Player: Asker}, Request{Kind: KindStanding}},
		// Somebody said "I": the asker stands.
		{"hur lång är min svit?", Request{Kind: KindStreak, Player: Asker}, Request{Kind: KindStreak, Player: Asker}},
		{"how am I doing?", Request{Kind: KindStanding, Player: Asker}, Request{Kind: KindStanding, Player: Asker}},
		{"do I have a streak?", Request{Kind: KindStreak, Player: "Alma"}, Request{Kind: KindStreak, Player: "Alma"}},
		{"hur går det för Alma?", Request{Kind: KindStanding, Player: "Alma"}, Request{Kind: KindStanding, Player: "Alma"}},
		// The one player named is who it is about.
		{"hur går det för Bo?", Request{Kind: KindStanding, Player: Asker}, Request{Kind: KindStanding, Player: "Bo"}},
		{"hur lång är Bos svit?", Request{Kind: KindStreak}, Request{Kind: KindStreak, Player: "Bo"}},
		{"berätta om cid", Request{Kind: KindProfile, Player: "Dana"}, Request{Kind: KindProfile, Player: "Cid Larsson"}},
		{"vad fick Cid Larsson igår?", Request{Kind: KindScore}, Request{Kind: KindScore, Player: "Cid Larsson"}},
		// "Bo" is not "Bob", and a bot is neither.
		{"hur går det för Bob?", Request{Kind: KindStanding, Player: "Bo"}, Request{Kind: KindStanding, Player: "Bob"}},
		{"vem är bäst, bot?", Request{Kind: KindStanding, Player: Asker}, Request{Kind: KindStanding}},
		// A name the question does not say, and none it does: a nickname,
		// perhaps, and the model's reading stands.
		{"hur går det för Danne?", Request{Kind: KindStanding, Player: "Dana"}, Request{Kind: KindStanding, Player: "Dana"}},
		// The asker asking about themselves and naming another: theirs.
		{"har jag fler 2:or än Bo?", Request{Kind: KindCount, Player: Asker, Guesses: 2}, Request{Kind: KindCount, Player: Asker, Guesses: 2}},
		// Two names are the two compared; one is the opponent.
		{"vem är bäst av Cid och Dana?", Request{Kind: KindVersus, Player: Asker, Other: "Cid Larsson"},
			Request{Kind: KindVersus, Player: "Cid Larsson", Other: "Dana"}},
		{"hur står jag mot Bo?", Request{Kind: KindVersus, Player: Asker, Other: "Bo"},
			Request{Kind: KindVersus, Player: Asker, Other: "Bo"}},
		{"hur står jag mot Bo?", Request{Kind: KindVersus, Player: Asker, Other: Asker},
			Request{Kind: KindVersus, Player: "Bo"}},
		// "Vi" is the group's total for a count, unless it asks who.
		{"hur många 2:or har vi?", Request{Kind: KindCount, Guesses: 2}, Request{Kind: KindCount, Player: Group, Guesses: 2}},
		{"hur många 2:or har vi?", Request{Kind: KindCount, Player: Asker, Guesses: 2}, Request{Kind: KindCount, Player: Group, Guesses: 2}},
		{"vem av oss har flest 2:or?", Request{Kind: KindCount, Guesses: 2}, Request{Kind: KindCount, Guesses: 2}},
		// Two players share the name Dana here: one name said, not two
		// players compared.
		{"hur står jag mot Dana?", Request{Kind: KindVersus, Player: Asker, Other: "Dana"},
			Request{Kind: KindVersus, Player: Asker, Other: "Dana"}},
		// A kind that is about nobody is left alone.
		{"vem leder?", Request{Kind: KindLeader}, Request{Kind: KindLeader}},
	}
	for _, tc := range tests {
		got := ground(tc.read, Prompt{Question: tc.question, Asker: "Alma", Players: players})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read as %+v\n got %+v\nwant %+v", tc.question, tc.read, got, tc.want)
		}
	}
}

// A question that came with one of the bot's posts may be about a name or
// a "you" in the post, so the asker is not taken out of it.
func TestAQuotedPostMayBeWhatTheQuestionIsAbout(t *testing.T) {
	t.Parallel()
	p := Prompt{Question: "vad betyder det?", Asker: "Alma", Players: []string{"Alma", "Bo"}, Context: "Alma: 3/6"}
	if got := ground(Request{Kind: KindStanding, Player: Asker}, p); got.Player != Asker {
		t.Errorf("player = %q", got.Player)
	}
}

// A month is the one the question names. "Kan jag vinna månaden?" names
// none, and came back as last month's race more often than not.
func TestAMonthTheQuestionNeverNamedIsDropped(t *testing.T) {
	t.Parallel()
	for question, kept := range map[string]bool{
		"kan jag vinna månaden?":           false,
		"vem leder?":                       false,
		"hur många månader har Bo vunnit?": false,
		"vem vann förra månaden?":          true,
		"vem vann i augusti?":              true,
		"how did I do in Aug?":             true,
		"who won last month?":              true,
		"vem vann juli 2025?":              true,
	} {
		got := ground(Request{Kind: KindCatchup, Month: "2026-08"}, Prompt{Question: question})
		if (got.Month != "") != kept {
			t.Errorf("%s: month = %q", question, got.Month)
		}
	}
}

// A day the question names in a word is that day, whatever the model
// wrote: "on Saturday" came back as yesterday.
func TestADayTheQuestionNamesIsThatDay(t *testing.T) {
	t.Parallel()
	// A Tuesday.
	today := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	for question, want := range map[string]string{
		"what did Dana get on Saturday?": "2026-09-12",
		"vad fick Dana i lördags?":       "2026-09-12",
		"vad fick Dana igår?":            "2026-09-14",
		"vad fick Dana i förrgår?":       "2026-09-13",
		"hur gick gårdagens för Dana?":   "2026-09-14",
		"vad fick Dana i måndags?":       "2026-09-14",
		"vad fick Dana idag?":            "",
		"vad fick Dana i dag?":           "",
		// No word for a day: the model's reading stands.
		"vad fick Dana den 3 september?": "2026-09-03",
		"vad fick Dana?":                 "2026-09-03",
	} {
		got := ground(Request{Kind: KindScore, Player: "Dana", Date: "2026-09-03"},
			Prompt{Question: question, Players: []string{"Dana"}, Today: today})
		if got.Date != want {
			t.Errorf("%s: date = %q, want %q", question, got.Date, want)
		}
	}
	// A what-if's day is still to come, and a kind with no day takes none.
	got := ground(Request{Kind: KindWhatIf}, Prompt{Question: "om jag får en 3:a på fredag?", Today: today})
	if got.Date != "2026-09-18" {
		t.Errorf("a what-if on Friday: %q", got.Date)
	}
	got = ground(Request{Kind: KindToday}, Prompt{Question: "vem har inte postat idag?", Today: today})
	if got.Date != "" {
		t.Errorf("today's kind got a date: %q", got.Date)
	}
}

// A low average is the good end of the table: "vem har lägst snitt?" asks
// who is best, which the model read as who is last.
func TestTheLowestAverageIsTheBest(t *testing.T) {
	t.Parallel()
	for question, worst := range map[string]bool{
		"vem har lägst snitt totalt?":          false,
		"who has the lowest average?":          false,
		"vem har högst snitt den här månaden?": true,
	} {
		got := ground(Request{Kind: KindLeader, Worst: !worst}, Prompt{Question: question})
		if got.Worst != worst {
			t.Errorf("%s: worst = %v", question, got.Worst)
		}
	}
	// With neither word, the model's reading stands.
	if got := ground(Request{Kind: KindLeader, Worst: true}, Prompt{Question: "vem har sämst snitt?"}); !got.Worst {
		t.Error("a reading the words do not settle was changed")
	}
}

// A week the question names is that week: "den här veckan då?" after a
// question about all time came back as all time again.
func TestAWeekTheQuestionNamesIsThatWeek(t *testing.T) {
	t.Parallel()
	previous := Request{Kind: KindStanding, Span: SpanAll, Player: "Alma"}
	for question, want := range map[string]Span{
		"den här veckan då?":             SpanWeek,
		"och förra veckan?":              SpanLastWeek,
		"hur går det för mig this week?": SpanWeek,
		"who led last week?":             SpanLastWeek,
		// No week named: the model's reading stands.
		"hur går det för mig?":             SpanAll,
		"vilken veckodag är jag bäst på?":  SpanAll,
		"hur gick det för två veckor sen?": SpanAll,
	} {
		got := ground(Request{Kind: KindStanding, Span: SpanAll, Player: Asker},
			Prompt{Question: question, Asker: "Alma", Players: []string{"Alma", "Bo"}, Previous: &previous})
		if got.Span != want {
			t.Errorf("%s: span = %q, want %q", question, got.Span, want)
		}
	}
	// A kind with no span takes none.
	if got := ground(Request{Kind: KindStreak, Span: SpanMonth}, Prompt{Question: "min svit den här veckan?"}); got.Span != SpanMonth {
		t.Errorf("a streak got the span %q", got.Span)
	}
}

// A what-if with two people in it — "om Dana får X imorgon, kan jag gå
// om?" — is about the one named after "om": the asker is asking what it
// would mean for them, and the model gave them the score.
func TestAWhatIfIsAboutWhoeverFollowsIf(t *testing.T) {
	t.Parallel()
	players := []string{"Alma", "Bo", "Dana"}
	for question, want := range map[string]string{
		"om Dana får X imorgon, kan jag gå om?": "Dana",
		"if Bo gets a 6 tomorrow, do I lead?":   "Bo",
		"om jag får en 3:a, slår jag Bo?":       Asker,
		"om I get a 2 today, am I ahead of Bo?": Asker,
		"vad händer om Bo får en 1:a?":          "Bo",
	} {
		got := ground(Request{Kind: KindWhatIf, Scores: []Hypothetical{{Player: Asker, Guesses: 7}}},
			Prompt{Question: question, Asker: "Alma", Players: players})
		if len(got.Scores) != 1 || got.Scores[0].Player != want || got.Scores[0].Guesses != 7 {
			t.Errorf("%s: scores = %+v, want %s with 7", question, got.Scores, want)
		}
	}
	// Two scores are two people's, as the model read them.
	two := []Hypothetical{{Player: "Bo", Guesses: 7}, {Player: "Dana", Guesses: 3}}
	got := ground(Request{Kind: KindWhatIf, Scores: two}, Prompt{Question: "om Bo får X och Dana en 3:a?", Asker: "Alma", Players: players})
	if len(got.Scores) != 2 || got.Scores[0].Player != "Bo" {
		t.Errorf("two scores: %+v", got.Scores)
	}
}

// "Vem är bäst?" is the board, all time, whatever else the message asks:
// a month in the next question is not this one's.
func TestWhoIsBestWithNoPeriodIsAllTime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		question string
		want     Span
	}{
		{"avgör detta för oss. Vem är bäst? Vem kommer vinna månaden?", SpanAll},
		{"vem är bäst?", SpanAll},
		{"who is the best?", SpanAll},
		{"vem är bäst den här månaden?", SpanMonth},
		{"vem är bäst i augusti?", SpanMonth},
		{"vem har bäst snitt de senaste 14 dagarna?", SpanMonth},
		{"vem är bäst just nu?", SpanMonth},
		{"vem leder?", SpanMonth},
	} {
		got := ground(Request{Kind: KindLeader, Span: SpanMonth}, Prompt{Question: tc.question, Asker: "Alma", Players: []string{"Alma", "Bo"}})
		if got.Span != tc.want {
			t.Errorf("%q: span %q, want %q", tc.question, got.Span, tc.want)
		}
	}
	if got := ground(Request{Kind: KindLeader, Span: SpanMonth, Worst: true}, Prompt{Question: "vem är sämst?"}); got.Span != SpanMonth {
		t.Errorf("the other end moved: %+v", got)
	}
}
