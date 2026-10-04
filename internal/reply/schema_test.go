package reply

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// decoded is the schema as the server receives it.
func decoded(t *testing.T, players []string) (raw string, kinds []map[string]any) {
	t.Helper()
	data, err := json.Marshal(requestSchema(players))
	if err != nil {
		t.Fatalf("the schema does not marshal: %v", err)
	}
	var schema struct {
		AnyOf []map[string]any `json:"anyOf"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	return string(data), schema.AnyOf
}

// One alternative per kind, each requiring the kind and exactly its own
// fields, the kind written first: the fields after it depend on it, and a
// field that is required cannot be left out.
func TestTheSchemaHasEachKindWithItsOwnFields(t *testing.T) {
	t.Parallel()
	raw, kinds := decoded(t, []string{"Alma", "Bo"})
	if len(kinds) != len(Kinds) {
		t.Fatalf("%d alternatives for %d kinds", len(kinds), len(Kinds))
	}
	for i, k := range Kinds {
		fields, ok := kindFields[k]
		if !ok {
			t.Errorf("kind %q has no fields listed", k)
			continue
		}
		props := kinds[i]["properties"].(map[string]any)
		if props["kind"].(map[string]any)["const"] != string(k) {
			t.Errorf("alternative %d is not %q", i, k)
		}
		want := append(append([]string{"kind"}, fields...), "also")
		var required []string
		for _, r := range kinds[i]["required"].([]any) {
			required = append(required, r.(string))
		}
		if !reflect.DeepEqual(required, want) {
			t.Errorf("%s requires %v, want %v", k, required, want)
		}
		if len(props) != len(want) {
			t.Errorf("%s allows %d properties, requires %d", k, len(props), len(want))
		}
		// Further questions are requests of the same kinds without an
		// "also" of their own: one level deep, and bounded.
		also := props["also"].(map[string]any)
		further := also["items"].(map[string]any)["anyOf"].([]any)
		if len(further) != len(Kinds) || also["maxItems"] != float64(maxAlso) {
			t.Errorf("%s: %d further kinds, at most %v", k, len(further), also["maxItems"])
		}
		for _, f := range further {
			if _, nested := f.(map[string]any)["properties"].(map[string]any)["also"]; nested {
				t.Errorf("%s: a further question may carry further questions", k)
			}
		}
	}
	// The order is the document's, which a decoded map has lost.
	if at := strings.Index(raw, `{"type":"object","properties":{"kind":`); at < 0 {
		t.Errorf("the kind is not the first property written:\n%.300s", raw)
	}
	if strings.Contains(raw, "null") {
		t.Errorf("a field with no schema:\n%s", raw)
	}
}

// A player is a choice among the roster, the asker and anyone, and only a
// count can be about the group; with no roster there is nothing to choose
// among.
func TestThePlayerIsAChoice(t *testing.T) {
	t.Parallel()
	_, kinds := decoded(t, []string{"Alma", "Bo"})
	choices := func(k Kind, field string) []any {
		props := kinds[slices.Index(Kinds, k)]["properties"].(map[string]any)
		return props[field].(map[string]any)["enum"].([]any)
	}
	if got := choices(KindStreak, "player"); !reflect.DeepEqual(got, []any{Anyone, Asker, "Alma", "Bo"}) {
		t.Errorf("a streak's player: %v", got)
	}
	if got := choices(KindCount, "player"); !reflect.DeepEqual(got, []any{Anyone, Asker, Group, "Alma", "Bo"}) {
		t.Errorf("a count's player: %v", got)
	}
	if got := choices(KindVersus, "other"); !reflect.DeepEqual(got, []any{Asker, "Alma", "Bo"}) {
		t.Errorf("a versus's other: %v", got)
	}
	_, kinds = decoded(t, nil)
	props := kinds[slices.Index(Kinds, KindStreak)]["properties"].(map[string]any)
	if _, constrained := props["player"].(map[string]any)["enum"]; constrained {
		t.Error("a player is a choice among nobody")
	}
}

// What the model is made to write for a kind is what the parse keeps for
// it: a field required here and dropped there would be seconds of writing
// thrown away, and one kept there and never asked for could not be said.
func TestTheSchemaAndTheParseAgreeOnAKindsFields(t *testing.T) {
	t.Parallel()
	full := `"span":"week","worst":true,"player":"Bo","other":"Cid","topic":"miss","date":"2026-09-01",` +
		`"month":"2026-08","guesses":3,"orbetter":true,"scores":[{"player":"Bo","guesses":3}]`
	for _, k := range Kinds {
		got, err := parseRequest(`{"kind":"` + string(k) + `",` + full + `}`)
		if err != nil {
			t.Fatal(err)
		}
		takes := func(names ...string) bool {
			return slices.ContainsFunc(names, func(n string) bool { return slices.Contains(kindFields[k], n) })
		}
		for _, f := range []struct {
			name string
			kept bool
			want bool
		}{
			{"player", got.Player != "", takes("player")},
			{"other", got.Other != "", takes("other")},
			{"topic", got.Topic != "", takes("topic")},
			{"date", got.Date != "", takes("date")},
			{"guesses", got.Guesses != 0, takes("guesses")},
			{"orbetter", got.OrBetter, takes("orbetter")},
			{"worst", got.Worst, takes("worst", "easiest", "fewest")},
			{"scores", len(got.Scores) > 0, takes("scores")},
		} {
			if f.kept != f.want {
				t.Errorf("%s: %s kept by the parse is %v, asked for by the schema is %v", k, f.name, f.kept, f.want)
			}
		}
	}
}
