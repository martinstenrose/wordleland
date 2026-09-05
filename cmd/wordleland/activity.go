package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// activityListLimit bounds the default listing. An operator asking for the
// log wants the recent end of it, the same as the admin page; --limit
// widens that when more history is needed.
const activityListLimit = 50

func runActivity(e *env, args []string) error {
	return dispatch(e, "activity", []subcommand{
		{"list", "list what has been logged", activityList},
		{"show", "show one event's full detail", activityShow},
	}, args)
}

func activityList(e *env, args []string) error {
	fs := flagSet(e, "activity list")
	kind := fs.String("kind", "", "`results`, `players` or `users`; omit for all")
	limit := fs.Int("limit", activityListLimit, "maximum events to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *kind {
	case "", store.ActivityResults, store.ActivityPlayers, store.ActivityUsers:
	default:
		return fmt.Errorf("--kind must be %q, %q or %q",
			store.ActivityResults, store.ActivityPlayers, store.ActivityUsers)
	}
	if *limit <= 0 {
		return fmt.Errorf("--limit must be positive")
	}

	events, total, err := store.ListActivity(e.ctx, e.db, *kind, *limit)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Fprintln(e.out, "No activity.")
		return nil
	}

	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN\tKIND\tACTION\tACTOR\tSUBJECT")
	for _, ev := range events {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
			ev.ID, absoluteTime(ev.At), ev.Kind, ev.Action, activityActor(ev), activitySubject(ev))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if len(events) < total {
		fmt.Fprintf(e.out, "\nShowing %d of %d. Raise --limit or narrow --kind to see more.\n",
			len(events), total)
	}
	return nil
}

func activityShow(e *env, args []string) error {
	fs := flagSet(e, "activity show")
	id := fs.Int64("id", 0, "event id, from 'activity list'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return fmt.Errorf("--id is required")
	}

	ev, err := store.ActivityEvent(e.ctx, e.db, *id)
	if errors.Is(err, store.ErrEventNotFound) {
		return fmt.Errorf("no such activity event: %d", *id)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "ID:      %d\n", ev.ID)
	fmt.Fprintf(e.out, "When:    %s\n", absoluteTime(ev.At))
	fmt.Fprintf(e.out, "Kind:    %s\n", ev.Kind)
	fmt.Fprintf(e.out, "Action:  %s\n", ev.Action)
	fmt.Fprintf(e.out, "Actor:   %s\n", activityActor(ev))
	if subject := activitySubject(ev); subject != "" {
		fmt.Fprintf(e.out, "Subject: %s\n", subject)
	}
	if ev.Detail != "" {
		fmt.Fprintf(e.out, "Detail:\n%s\n", indentJSON(ev.Detail))
	}
	return nil
}

// activityActor names who made a change, the same three shapes the admin
// page shows: an email address, a token by its label, or the system acting
// on its own — there is no session here to read a display name from, so a
// raw address is as specific as the CLI can be.
func activityActor(ev store.Event) string {
	switch ev.ActorKind {
	case store.ActorToken:
		if ev.ActorToken != "" {
			return "token:" + ev.ActorToken
		}
		return "token"
	case store.ActorSystem:
		return "system"
	default:
		return ev.ActorEmail
	}
}

// activitySubject names what an event was about. The slug is preferred
// because it is what an operator would use to act on the row further; a
// bare id is the last resort, for a subject that no longer exists.
func activitySubject(ev store.Event) string {
	if ev.SubjectSlug != "" {
		return ev.SubjectSlug
	}
	if ev.SubjectID != nil {
		return ev.SubjectType + " #" + strconv.FormatInt(*ev.SubjectID, 10)
	}
	return ""
}

// absoluteTime spells a timestamp out in the deployment's own clock, with
// its offset: a row written before TZ was set, or on the other side of a
// DST change, still reads correctly, and the offset lets it be compared
// against a Signal timestamp without guessing which zone either is in.
func absoluteTime(at time.Time) string {
	return at.Local().Format("2006-01-02 15:04:05 -0700")
}

// indentJSON pretty-prints the stored detail, leaving it untouched when it
// will not parse: the point of showing it is that it is what is stored, so
// a value that fails to parse must still be visible rather than hidden.
func indentJSON(raw string) string {
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return out.String()
}
