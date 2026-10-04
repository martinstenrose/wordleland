package reply

import (
	"slices"
	"strings"
	"unicode"
)

// ground holds what the model read against the question's own words, for
// the one thing a small model gets wrong more than any other: who a
// question is about. It fills the asker in for "vem har längst svit?", and
// picks a name the question never said. The question says who it is about
// in words Go can read as well as the model can — a player's name, "jag",
// "my", "vi" — so a reading the words do not support is corrected here
// rather than answered. Only ever toward what the question says: a name or
// a pronoun that is there wins over one that is not, and where the words
// settle nothing the model's reading stands.
func ground(r Request, p Prompt) Request {
	q := read(p)
	if q.day != "" && slices.Contains(kindFields[r.Kind], "date") {
		// The day the question says, in a word Go knows: "i lördags" is
		// last Saturday whatever the model made of it.
		if date, ok := dayOf(q.day, r.Kind == KindWhatIf, p.Today); ok {
			r.Date = date
			if date == p.Today.Format(DateLayout) {
				r.Date = ""
			}
		}
	}
	if r.Kind == KindLeader && q.lowest != q.highest {
		// A low average is the good end: "vem har lägst snitt?" asks who
		// is best, and "högst snitt" who is last.
		r.Worst = q.highest
	}
	if r.Month != "" && !q.month && !q.quoted {
		// A month the question never named: "kan jag vinna månaden?" is
		// the month now running, whatever the model made of it.
		r.Month = ""
	}
	r = q.who(r)
	for i, a := range r.Also {
		// A further question shares the message's names with the first,
		// so only the asker is checked: "me" needs somebody saying "me".
		if q.isAsker(a.Player) && !q.supportsAsker() {
			r.Also[i].Player = ""
		}
	}
	return r
}

// reading is what a question's words say about who it concerns.
type reading struct {
	asker string
	// named are the players the question names, in the order it does.
	named []string
	// first, group and asks say the question has a word for the asker
	// ("jag", "my"), for the group as a whole ("vi", "we"), and one asking
	// which player ("vem", "who").
	first, group, asks bool
	// quoted says the question came with one of the bot's posts, whose
	// names and scores it may be about without repeating them.
	quoted bool
	// day is the day the question names in a word, as the model's word
	// for it: "igår" is "yesterday", "i lördags" is "saturday".
	day string
	// lowest and highest say the question asks for the lowest or the
	// highest average.
	lowest, highest bool
	// month says the question names a month: by name, as "förra
	// månaden", or in figures.
	month bool
}

var (
	firstWords = []string{"jag", "mig", "mej", "min", "mitt", "mina", "me", "my", "mine", "myself"}
	wholeWords = []string{"vi", "oss", "vår", "vårt", "våra", "gruppen", "we", "us", "our", "group", "tillsammans", "totalt"}
	whoWords   = []string{"vem", "vilka", "vilken", "who", "which", "whose", "vems"}
	// monthNames are how a month is said, in the catalogue's languages,
	// long and short; monthBefore, the words that make "månaden" another
	// month than this one.
	monthNames = []string{"januari", "februari", "mars", "april", "maj", "juni", "juli", "augusti",
		"september", "oktober", "november", "december", "january", "february", "march", "may", "june",
		"july", "august", "october", "jan", "feb", "mar", "apr", "jun", "jul", "aug", "sep", "sept",
		"okt", "oct", "nov", "dec"}
	// dayNames are the words for a day, each with the model's word for it.
	dayNames = map[string]string{
		"idag": wordToday, "dagens": wordToday, "today": wordToday,
		"igår": wordYesterday, "gårdagens": wordYesterday, "yesterday": wordYesterday,
		"förrgår": wordDayBefore, "imorgon": wordTomorrow, "tomorrow": wordTomorrow,
		"måndag": "monday", "tisdag": "tuesday", "onsdag": "wednesday", "torsdag": "thursday",
		"fredag": "friday", "lördag": "saturday", "söndag": "sunday",
	}
	averageWords = []string{"snitt", "snittet", "genomsnitt", "average"}
	monthBefore  = []string{"förra", "förrförra", "föregående", "senaste", "last", "previous", "innan", "before"}
	// afterI are words that make an "i" before them the English pronoun
	// rather than the Swedish preposition, which is the commonest word in
	// "vem leder i september?".
	afterI = []string{"am", "m", "have", "ve", "do", "did", "can", "could", "will", "ll", "got", "get",
		"need", "still", "win", "won", "lead", "ever", "stand", "rank"}
)

func read(p Prompt) reading {
	q := reading{asker: p.Asker, quoted: p.Context != ""}
	words := strings.FieldsFunc(p.Question, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) })
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	for i, w := range lower {
		switch {
		case slices.Contains(firstWords, w):
			q.first = true
		case w == "i" && (words[i] == "I" && i > 0 || i+1 < len(lower) && slices.Contains(afterI, lower[i+1])):
			q.first = true
		case slices.Contains(wholeWords, w):
			q.group = true
		case slices.Contains(whoWords, w):
			q.asks = true
		}
	}
	average := slices.ContainsFunc(lower, func(w string) bool { return slices.Contains(averageWords, w) })
	for i, w := range lower {
		// "måndags", "i lördags": the day with its ending.
		name := strings.TrimSuffix(strings.TrimSuffix(w, "s"), "en")
		switch word, ok := dayNames[w]; {
		case q.day != "":
		case ok:
			q.day = word
		case dayNames[name] != "":
			q.day = dayNames[name]
		case slices.Contains(weekdayWords, w):
			q.day = w
		case w == "morgon" && i > 0 && lower[i-1] == "i":
			q.day = wordTomorrow
		case w == "dag" && i > 0 && lower[i-1] == "i":
			q.day = wordToday
		}
		q.lowest = q.lowest || average && (w == "lägst" || w == "lägsta" || w == "lowest")
		q.highest = q.highest || average && (w == "högst" || w == "högsta" || w == "highest")
	}
	months := slices.ContainsFunc(lower, func(w string) bool {
		return strings.HasPrefix(w, "månad") || strings.HasPrefix(w, "month")
	})
	for _, w := range lower {
		_, figures := strings.CutPrefix(w, "20")
		q.month = q.month || slices.Contains(monthNames, w) || months && slices.Contains(monthBefore, w) ||
			figures && len(w) == 4
	}
	// A player is named by their whole name, or by a first name nobody
	// else has, with or without a genitive s: "Bos svit".
	firsts := map[string]int{}
	for _, name := range p.Players {
		first, _, _ := strings.Cut(strings.ToLower(name), " ")
		firsts[first]++
	}
	text := " " + strings.Join(lower, " ") + " "
	type hit struct {
		at   int
		name string
	}
	var hits []hit
	for _, name := range p.Players {
		full := strings.Join(strings.FieldsFunc(strings.ToLower(name), func(c rune) bool {
			return !unicode.IsLetter(c) && !unicode.IsDigit(c)
		}), " ")
		first, _, _ := strings.Cut(full, " ")
		forms := []string{full}
		if firsts[first] == 1 {
			forms = append(forms, first)
		}
		at := -1
		for _, form := range forms {
			for _, suffix := range []string{" ", "s "} {
				if i := strings.Index(text, " "+form+suffix); i >= 0 && (at < 0 || i < at) {
					at = i
				}
			}
		}
		if at >= 0 {
			hits = append(hits, hit{at, name})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return a.at - b.at })
	for _, h := range hits {
		// Two players of one name are one name said: which of them is
		// the answer's to ask.
		if !q.names(h.name) {
			q.named = append(q.named, h.name)
		}
	}
	return q
}

// isAsker says a player field means the person asking.
func (q reading) isAsker(player string) bool {
	return player == Asker || q.asker != "" && strings.EqualFold(player, q.asker)
}

// supportsAsker says the question could be about the asker: it says "I"
// or their name, or came with a post that may.
func (q reading) supportsAsker() bool {
	return q.first || q.quoted || q.names(q.asker)
}

func (q reading) names(player string) bool {
	return slices.ContainsFunc(q.named, func(n string) bool { return strings.EqualFold(n, player) })
}

// who settles the players of a request by the question's words.
func (q reading) who(r Request) Request {
	if !slices.Contains(kindFields[r.Kind], "player") {
		return r
	}
	if r.Kind == KindVersus {
		switch {
		case len(q.named) >= 2:
			// Two names are the two compared, whoever is asking.
			r.Player, r.Other = q.named[0], q.named[1]
		case len(q.named) == 1 && !q.names(r.Player) && !q.names(r.Other):
			// One name and it is in neither place: it is the opponent.
			r.Player, r.Other = q.named[0], ""
		}
		return r
	}
	unsupported := q.isAsker(r.Player) && !q.supportsAsker()
	switch {
	case r.Player == Group:
	case len(q.named) == 1 && !q.names(r.Player) && (r.Player == "" || unsupported || !q.isAsker(r.Player) && !q.quoted):
		// The one player the question names is who it is about, when the
		// model wrote nobody, an asker who never said "I", or somebody
		// the question does not mention.
		r.Player = q.named[0]
	case unsupported:
		// "Vem har längst svit?" is not about whoever asks it.
		r.Player = ""
	}
	if r.Kind == KindCount && r.Player == "" && r.Guesses > 0 && q.group && !q.asks {
		// "Hur många 2:or har vi?" is the group's total, not a ranking.
		r.Player = Group
	}
	return r
}
