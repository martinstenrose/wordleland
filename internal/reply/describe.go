package reply

import (
	"encoding/json"
	"fmt"
)

// Describe is a request as JSON with only the fields that say something,
// further questions included: how `wordleland ask` shows a placing, and
// how the model is shown the request a follow-up follows.
func Describe(req Request) string {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("%+v", req)
	}
	var fields any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return string(raw)
	}
	out, _ := json.Marshal(pruned(fields))
	return string(out)
}

// pruned drops the zero values from decoded JSON, at every depth, and
// reports nil for a value that says nothing at all.
func pruned(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, field := range v {
			if p := pruned(field); p == nil {
				delete(v, k)
			} else {
				v[k] = p
			}
		}
		return v
	case []any:
		if len(v) == 0 {
			return nil
		}
		for i, item := range v {
			v[i] = pruned(item)
		}
		return v
	case string:
		if v == "" {
			return nil
		}
	case float64:
		if v == 0 {
			return nil
		}
	case bool:
		if !v {
			return nil
		}
	case nil:
		return nil
	}
	return v
}
