package reply

import (
	"bytes"
	"encoding/json"
)

// kindFields is what each kind of question needs said, in the order the
// model writes it. A kind's fields are all required and no others are
// allowed, so the model writes exactly what the answer will read: with
// every field required for every kind it wrote fourteen for "vem leder?",
// most of a CPU's seconds; with every field optional it left out the ones
// that mattered. The parse's own rules for which field means something to
// which kind (normalise) are the other half of this table, and a test
// holds the two together.
var kindFields = map[Kind][]string{
	KindLeader:   {"span", "worst", "tone"},
	KindStanding: {"player", "span", "tone"},
	KindStreak:   {"player"},
	KindToday:    {},
	KindScore:    {"player", "date"},
	KindWins:     {"player", "month"},
	KindCatchup:  {"player", "month", "tone"},
	KindCount:    {"player", "guesses", "orbetter", "fewest"},
	KindHabits:   {"player"},
	KindWhatIf:   {"scores", "date"},
	KindVersus:   {"player", "other", "span"},
	KindDay:      {"date"},
	KindPuzzles:  {"span", "easiest"},
	KindDayWins:  {"player", "span"},
	KindForm:     {"player", "worst", "span", "tone"},
	KindSteady:   {"player"},
	KindWeekday:  {"player"},
	KindProfile:  {"player", "tone"},
	KindHistory:  {"player"},
	KindRecords:  {},
	KindGroup:    {},
	KindRules:    {"topic"},
	KindThanks:   {},
	KindHelp:     {},
	KindUnknown:  {},
}

// ordered is a JSON object whose keys keep the order they were given. The
// server turns the schema into a grammar that writes properties in the
// order the schema lists them, and encoding/json sorts a map's keys: the
// kind has to come first, since the fields after it depend on it.
type ordered []member

type member struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(m.value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Anyone is what the model writes for the player of a question that asks
// who — "vem har längst svit?" — or names nobody: the answer is about every
// player. A word rather than an empty string because a small model fills
// an empty field in, with the asker more often than not, and picks a word
// that says what it means.
const Anyone = "anyone"

// requestSchema is handed to the server as the response format, so the
// model is constrained to a Request rather than asked nicely for one: one
// alternative per kind, each the kind and then its own fields. A player is
// one of the names it was given, the asker, or anyone; a day, a month and
// a span are words from short lists or have the shape of what they are. A
// value outside these cannot be produced, and the parse still checks.
func requestSchema(players []string) ordered {
	who := func(words ...string) any {
		if len(players) == 0 {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "string", "enum": append(words, players...)}
	}
	word := func(words []string, patterns ...string) any {
		shapes := []any{map[string]any{"type": "string", "enum": words}}
		for _, p := range patterns {
			shapes = append(shapes, map[string]any{"type": "string", "pattern": p})
		}
		return map[string]any{"anyOf": shapes}
	}
	flag := map[string]any{"type": "boolean"}
	fields := map[string]any{
		// One word, so that it is one decision: three fields — a span, its
		// days, a month — came back contradicting each other.
		"span":     word(periodWords, daysPattern, monthPattern),
		"month":    word(append([]string{wordNoMonth, wordLastMonth}, monthWords...), monthPattern),
		"date":     word(dayWords, datePattern, puzzlePattern),
		"worst":    flag,
		"easiest":  flag,
		"fewest":   flag,
		"orbetter": flag,
		"player":   who(Anyone, Asker),
		"other":    who(Asker),
		"topic":    map[string]any{"type": "string", "enum": enum(Topics)},
		// Asked for only of the kinds an answer can meet in kind: the
		// race, a player's profile, and their form.
		"tone":    map[string]any{"type": "string", "enum": enum(Tones)},
		"guesses": map[string]any{"type": "integer"},
		"scores": map[string]any{"type": "array", "items": ordered{
			{"type", "object"},
			{"properties", ordered{
				{"player", who(Asker)},
				{"guesses", map[string]any{"type": "integer"}},
			}},
			{"required", []string{"player", "guesses"}},
		}},
	}
	one := func(k Kind, also any) ordered {
		props := ordered{{"kind", map[string]any{"const": string(k)}}}
		required := []string{"kind"}
		for _, f := range kindFields[k] {
			field := fields[f]
			if f == "player" && k == KindCount {
				// Only a count has a figure for the group as a whole.
				field = who(Anyone, Asker, Group)
			}
			props = append(props, member{f, field})
			required = append(required, f)
		}
		if also != nil {
			props = append(props, member{"also", also})
			required = append(required, "also")
		}
		return ordered{{"type", "object"}, {"properties", props}, {"required", required}}
	}
	// The further questions of a message that asks more than one: each a
	// request of its own kind, one level deep.
	var further []any
	for _, k := range Kinds {
		further = append(further, one(k, nil))
	}
	also := ordered{{"type", "array"}, {"maxItems", maxAlso}, {"items", ordered{{"anyOf", further}}}}
	var kinds []any
	for _, k := range Kinds {
		kinds = append(kinds, one(k, also))
	}
	return ordered{{"anyOf", kinds}}
}
