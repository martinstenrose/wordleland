package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

func runQuestions(e *env, args []string) error {
	return dispatch(e, "questions", []subcommand{
		{"list", "list questions the Signal bot could not answer (kept 30 days)", questionsList},
	}, args)
}

// questionsList is the owner's to-do list for the bot: what the group asked
// that it could not place, newest first. Text and date only — there is no
// sender to show.
func questionsList(e *env, args []string) error {
	fs := flagSet(e, "questions list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	questions, err := store.ListUnansweredQuestions(e.ctx, e.db)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		fmt.Fprintln(e.out, "No unanswered questions.")
		return nil
	}

	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ASKED\tQUESTION")
	for _, q := range questions {
		fmt.Fprintf(w, "%s\t%s\n", q.AskedAt.Local().Format(time.DateOnly), q.Question)
	}
	return w.Flush()
}
