package reply

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The model writes a request in words of its own where the Request has a
// figure it would have to work out: "yesterday" for a date, "august" for a
// month, "14d" for a number of days, a puzzle by its number. A small model
// that is asked for "förra månaden" as YYYY-MM gets the arithmetic wrong
// often and a word right nearly always, so the words are its vocabulary
// and the arithmetic is done here, against the date the question was
// asked on.

// Words for a day, besides a date as YYYY-MM-DD and a puzzle's number.
const (
	wordToday     = "today"
	wordYesterday = "yesterday"
	wordDayBefore = "daybeforeyesterday"
	wordTomorrow  = "tomorrow"
)

// wordLastMonth is the month before the running one, besides a month's
// English name and a month as YYYY-MM. wordNoMonth is no month named: a
// word, because offered an empty string or "lastmonth" the model took
// "lastmonth" for "kan jag vinna månaden?" nearly every time.
const (
	wordLastMonth = "lastmonth"
	wordNoMonth   = "none"
)

var (
	weekdayWords = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	monthWords   = []string{"january", "february", "march", "april", "may", "june", "july",
		"august", "september", "october", "november", "december"}
)

// dayWords and periodWords are the schema's enums for a date and a span.
var (
	dayWords    = append([]string{"", wordToday, wordYesterday, wordDayBefore, wordTomorrow}, weekdayWords...)
	periodWords = append([]string{string(SpanMonth), string(SpanAll), string(SpanWeek), string(SpanLastWeek),
		wordLastMonth}, monthWords...)
)

// The shapes of what is written out instead: a date, a month, a number of
// days, a puzzle's number.
const (
	datePattern   = "^([0-9]{4}-)?[0-9]{2}-[0-9]{2}$"
	monthPattern  = "^[0-9]{4}-[0-9]{2}$"
	daysPattern   = "^[1-9][0-9]{0,2}d$"
	puzzlePattern = "^[1-9][0-9]{2,4}$"
)

// aliases are a kind's own names for the Request's Worst, which means the
// easiest puzzle and the fewest of a score: "worst" for "the hardest
// puzzle" was read as true as often as not.
type aliases struct {
	Easiest bool      `json:"easiest"`
	Fewest  bool      `json:"fewest"`
	Also    []aliases `json:"also"`
}

// expand turns the model's words into the Request's own values. today is
// the day the question was asked; without one the words that need it name
// nothing, and the request falls back to its defaults.
func expand(r Request, a aliases, today time.Time) Request {
	r.Worst = r.Worst || a.Easiest || a.Fewest

	switch span := strings.TrimSpace(string(r.Span)); {
	case strings.HasSuffix(span, "d"):
		if n, err := strconv.Atoi(strings.TrimSuffix(span, "d")); err == nil {
			r.Span, r.Days = SpanDays, n
		}
	default:
		if _, err := time.Parse(MonthLayout, span); err == nil {
			r.Span, r.Month = SpanMonth, span
		} else if month, ok := monthOf(span, today); ok {
			r.Span, r.Month = SpanMonth, month
		}
	}
	if month, ok := monthOf(strings.TrimSpace(r.Month), today); ok {
		r.Month = month
	} else if strings.EqualFold(strings.TrimSpace(r.Month), wordNoMonth) {
		r.Month = ""
	}
	if !today.IsZero() && r.Month == today.Format(MonthLayout) {
		// The month now running is the month, however it was named.
		r.Month = ""
	}

	date := strings.ToLower(strings.TrimSpace(r.Date))
	if puzzle, err := strconv.Atoi(date); err == nil && r.Puzzle == 0 {
		r.Puzzle, r.Date = puzzle, ""
	} else if day, ok := dayOf(date, r.Kind == KindWhatIf, today); ok {
		r.Date = day
	} else if day, ok := dateOf(date, r.Kind == KindWhatIf, today); ok {
		r.Date = day
	}
	if !today.IsZero() && r.Date == today.Format(DateLayout) {
		r.Date = ""
	}
	return r
}

// monthOf is a month word as YYYY-MM: last month, or the most recent
// month of that name, the running one included.
func monthOf(word string, today time.Time) (string, bool) {
	word = strings.ToLower(word)
	if today.IsZero() {
		return "", false
	}
	if word == wordLastMonth {
		return monthsAgo(today, 1).Format(MonthLayout), true
	}
	i := slices.Index(monthWords, word)
	if i < 0 {
		return "", false
	}
	back := (int(today.Month()) - (i + 1) + 12) % 12
	return monthsAgo(today, back).Format(MonthLayout), true
}

// dayOf is a day word as YYYY-MM-DD. A weekday is the last one before
// today — "i fredags" — except for a what-if, whose days are still to
// come: there it is the next one.
func dayOf(word string, ahead bool, today time.Time) (string, bool) {
	if today.IsZero() {
		return "", false
	}
	days := 0
	switch word {
	case wordToday:
	case wordYesterday:
		days = -1
	case wordDayBefore:
		days = -2
	case wordTomorrow:
		days = 1
	default:
		i := slices.Index(weekdayWords, word)
		if i < 0 {
			return "", false
		}
		// time.Weekday counts from Sunday; the words from Monday.
		want := time.Weekday((i + 1) % 7)
		if ahead {
			days = (int(want)-int(today.Weekday())+6)%7 + 1
		} else {
			days = -((int(today.Weekday())-int(want)+6)%7 + 1)
		}
	}
	return today.AddDate(0, 0, days).Format(DateLayout), true
}

// dateOf is a day of a month as a date: the last time it came round, or
// for a what-if the next. The model writes "5 juli" as "07-05"; a year it
// wrote anyway is kept when the date has happened — "5 juli 2025" — and
// otherwise was made up, which it does, and the day and month are what
// the question said.
func dateOf(word string, ahead bool, today time.Time) (string, bool) {
	if today.IsZero() {
		return "", false
	}
	full, err := time.ParseInLocation(DateLayout, word, today.Location())
	if err != nil {
		full, err = time.ParseInLocation(DateLayout, strconv.Itoa(today.Year())+"-"+word, today.Location())
		if err != nil {
			return "", false
		}
	} else if !ahead && !full.After(today) || ahead && !full.Before(today.AddDate(0, 0, -1)) && full.Before(today.AddDate(1, 0, 0)) {
		return word, true
	}
	day := time.Date(today.Year(), full.Month(), full.Day(), 0, 0, 0, 0, today.Location())
	switch {
	case !ahead && day.After(today):
		day = day.AddDate(-1, 0, 0)
	case ahead && day.Before(today.AddDate(0, 0, -1)):
		day = day.AddDate(1, 0, 0)
	}
	return day.Format(DateLayout), true
}

// monthsAgo is the first day of the month n months before now's. From the
// first, since a month back from the 31st is not always a month.
func monthsAgo(now time.Time, n int) time.Time {
	return time.Date(now.Year(), now.Month()-time.Month(n), 1, 0, 0, 0, 0, now.Location())
}

// inWords is a request as the model would have written it: its kind and
// that kind's fields, in the model's own vocabulary. It is how the model is
// shown the request a follow-up follows, so that what it reads and what it
// writes are the same language.
func inWords(r Request) string {
	out := ordered{{"kind", string(r.Kind)}}
	for _, field := range kindFields[r.Kind] {
		var value any
		switch field {
		case "span":
			switch {
			case r.Month != "":
				value = r.Month
			case r.Span == SpanDays:
				value = strconv.Itoa(r.Days) + "d"
			case r.Span == "":
				value = string(SpanMonth)
			default:
				value = string(r.Span)
			}
		case "month":
			value = r.Month
			if r.Month == "" {
				value = wordNoMonth
			}
		case "date":
			value = r.Date
		case "worst", "easiest", "fewest":
			value = r.Worst
		case "player":
			value = r.Player
			if r.Player == "" {
				value = Anyone
			}
		case "other":
			value = r.Other
			if r.Other == "" {
				value = Asker
			}
		case "topic":
			value = string(r.Topic)
		case "guesses":
			value = r.Guesses
		case "orbetter":
			value = r.OrBetter
		case "scores":
			value = r.Scores
		}
		out = append(out, member{field, value})
	}
	text, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(text)
}
