package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// detailField is one line of the breakdown: a label, the value now, and
// what it replaced when the change had a before.
type detailField struct {
	Label string
	From  string
	To    string
}

// Changed reports whether the field records a transition rather than a
// standing value, so the template can draw the arrow only where one means
// something.
func (f detailField) Changed() bool { return f.From != "" }

type activityDetailPage struct {
	chrome

	Row    activityRow
	Fields []detailField

	// Raw is the stored JSON, shown last and collapsed. Every renderer
	// above it is a best effort at making one shape readable; this is the
	// thing that is actually recorded.
	Raw string

	BackHref string
}

// handleAdminActivityDetail shows what a logged event actually changed.
func (s *Server) handleAdminActivityDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	event, err := store.ActivityEvent(r.Context(), s.db, id)
	if errors.Is(err, store.ErrEventNotFound) {
		s.renderError(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("read activity event", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page := activityDetailPage{
		chrome:   s.adminChrome(w, r, "activity"),
		BackHref: "/admin/activity",
	}
	page.Row = s.activityRowFor(event, page.T)

	var detail map[string]any
	if event.Detail != "" {
		_ = json.Unmarshal([]byte(event.Detail), &detail)
		page.Raw = indentJSON(event.Detail)
	}
	page.Fields = detailFields(event, detail, page.T)

	s.render(w, r, http.StatusOK, "admin_activity_detail.html", page)
}

// detailFields turns the stored JSON into lines a person can read.
//
// Rendered from the shape rather than from a table of known actions: a
// {"from":…,"to":…} pair is a change whatever field it describes, so an
// action added later gets a readable page without anything being added
// here. Only the score, which is a scoreline rather than a value, needs
// knowing about.
func detailFields(event store.Event, detail map[string]any, t translator) []detailField {
	var fields []detailField

	if event.SubjectSlug != "" {
		fields = append(fields, detailField{
			Label: t.T("activity.detail.subject"),
			To:    event.SubjectSlug,
		})
	}

	if event.Kind == store.ActivityResults {
		if score := scorelineFrom(detail); score != "" {
			f := detailField{Label: t.T("activity.detail.score"), To: score}
			if prev, ok := detail["previous"].(map[string]any); ok {
				f.From = scorelineFrom(prev)
			}
			fields = append(fields, f)
		}
	}

	for _, key := range sortedKeys(detail) {
		switch key {
		case "previous", "puzzle_no", "solved", "guesses", "hard_mode":
			// Already said above, as a scoreline.
			continue
		}
		value := detail[key]
		if pair, ok := value.(map[string]any); ok {
			from, hasFrom := pair["from"]
			to, hasTo := pair["to"]
			if hasFrom && hasTo {
				fields = append(fields, detailField{
					Label: detailLabel(t, key),
					From:  scalarString(from),
					To:    scalarString(to),
				})
				continue
			}
		}
		if str := scalarString(value); str != "" {
			fields = append(fields, detailField{Label: detailLabel(t, key), To: str})
		}
	}
	return fields
}

// scorelineFrom renders a result the way the group writes it.
func scorelineFrom(detail map[string]any) string {
	solved, ok := detail["solved"].(bool)
	if !ok {
		return ""
	}
	guesses := "X"
	if solved {
		n, ok := detail["guesses"].(float64)
		if !ok {
			return ""
		}
		guesses = strconv.Itoa(int(n))
	}
	line := guesses + "/" + strconv.Itoa(wordle.MaxGuesses)
	if hard, _ := detail["hard_mode"].(bool); hard {
		line += "*"
	}
	return line
}

// detailLabel prefers translated copy and falls back to the stored key, so
// a field nobody has written copy for is still readable rather than blank.
func detailLabel(t translator, key string) string {
	if label := t.T("activity.detail." + key); label != "activity.detail."+key {
		return label
	}
	return strings.ReplaceAll(key, "_", " ")
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// indentJSON pretty-prints the stored detail, leaving it untouched when it
// will not parse — the point of showing it raw is that it is what is
// stored, so a value that is not valid JSON must still be visible.
func indentJSON(raw string) string {
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return out.String()
}

// absoluteTime spells a timestamp out, and is the only place that decides
// how. The list and the detail page both call it, because two of these
// drifted apart once already: one applied Local and the other did not, and
// they agreed only for as long as the container's timezone matched the one
// that wrote the row.
//
// Local, because the reader wants the deployment's clock rather than
// whatever offset happened to be stored — a row written before TZ was set,
// or on the other side of a DST change, still reads correctly. And the
// offset is printed, because "09:15" alone leaves an admin comparing this
// against a Signal timestamp with no way to tell CEST from UTC.
func absoluteTime(at time.Time) string {
	return at.Local().Format("2006-01-02 15:04:05 -0700")
}
