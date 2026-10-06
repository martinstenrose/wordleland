package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/reply"
)

// prepareFor is how long to wait for a model by default: pulling one the
// server does not have takes minutes on an ordinary connection. A name the
// server cannot pull is only found out by waiting, which is what --wait
// shortens.
const prepareFor = 30 * time.Minute

// runPlacingTest asks each model the placing test's questions and reports
// how many it placed as they should be, and how long it took. It needs the
// model server, not the database, so it runs before the database is
// opened, as version does.
func runPlacingTest(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("placing-test", flag.ContinueOnError)
	fs.SetOutput(out)
	models := fs.String("model", envOrDefault("LLM_MODEL", config.DefaultLLMModel),
		"the `models` to test, comma-separated; pulled when the server lacks one")
	url := fs.String("url", envOrDefault("LLM_URL", config.DefaultLLMURL), "the model server's `URL`")
	all := fs.Bool("all", false, "list every question, not only the misses")
	wait := fs.Duration("wait", prepareFor, "how long to wait for a model to be pulled and loaded")
	fs.Usage = func() { printFlags(out, "placing-test", fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	var summaries []string
	for _, model := range strings.Split(*models, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		summary, err := placingTestOne(ctx, out, *url, model, *all, *wait)
		if err != nil {
			return err
		}
		summaries = append(summaries, summary)
	}
	if len(summaries) > 1 {
		fmt.Fprintln(out, "\nSummary:")
		for _, s := range summaries {
			fmt.Fprintln(out, "  "+s)
		}
	}
	return nil
}

func placingTestOne(ctx context.Context, out io.Writer, url, model string, all bool, wait time.Duration) (string, error) {
	o, err := readyModel(ctx, out, url, model, wait)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "%s: asking %d questions as %s, on %s…\n", model, len(reply.PlacingCases),
		reply.PlacingPrompt("").Asker, reply.PlacingToday.Format("Monday 2 January 2006"))

	// The server's own timings, one per question in order: the questions
	// are asked one at a time.
	var usage []reply.Usage
	o.OnUsage = func(u reply.Usage) { usage = append(usage, u) }
	results := reply.RunPlacing(ctx, o, reply.PlacingCases, func(r reply.PlacingResult) {
		switch {
		case r.Err != nil:
			fmt.Fprintf(out, "  ! %5.1fs  %s — %v\n", r.Took.Seconds(), r.Case.Question, r.Err)
		case !r.Placed():
			fmt.Fprintf(out, "  ✗ %5.1fs  %s — %s\n", r.Took.Seconds(), r.Case.Question, strings.Join(r.Misses, "; "))
		case all:
			fmt.Fprintf(out, "  ✓ %5.1fs  %s\n", r.Took.Seconds(), r.Case.Question)
		}
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}

	placed := 0
	var took []time.Duration
	for _, r := range results {
		if r.Placed() {
			placed++
		}
		took = append(took, r.Took)
	}
	verdict, short := cacheVerdict(usage)
	summary := fmt.Sprintf("%s: %d of %d placed (%d%%), %s", model, placed, len(results),
		100*placed/max(len(results), 1), timing(took))
	if short != "" {
		summary += ", " + short
	}
	fmt.Fprintln(out, summary)
	if verdict != "" {
		fmt.Fprintln(out, "  "+verdict)
	}
	return summary, nil
}

// cacheVerdict reads the server's timings for whether it reuses its work on
// the instructions, which are the same for every question: the first
// question reads them all, and with reuse every later one reads only its
// own few words. A later median at a quarter of the first or less is reuse;
// a bot answering in seconds rather than tens of them depends on it.
//
// The first question may find the instructions already read — the bot
// starts its prompt with the same ones, and so did a run a minute ago —
// and then every question is quick, the first one too or nearly. Reading
// the whole prompt in under a second is beyond a CPU without reuse, so a
// later median under a second is reuse whatever the first took.
func cacheVerdict(usage []reply.Usage) (string, string) {
	if len(usage) < 2 {
		return "", ""
	}
	first := usage[0].Reading
	reading := make([]time.Duration, 0, len(usage)-1)
	writing := make([]time.Duration, 0, len(usage)-1)
	for _, u := range usage[1:] {
		reading = append(reading, u.Reading)
		writing = append(writing, u.Writing)
	}
	slices.Sort(reading)
	slices.Sort(writing)
	after, write := reading[len(reading)/2], writing[len(writing)/2]
	switch {
	case after*4 <= first:
		return fmt.Sprintf("Prompt cache: works — reading the prompt took %.1f s the first time and %.1f s after (median). Writing: median %.1f s.",
			first.Seconds(), after.Seconds(), write.Seconds()), "cache works"
	case after < time.Second:
		return fmt.Sprintf("Prompt cache: works — every question read its prompt in under a second (the first %.1f s), the instructions already read by an earlier question or run. Writing: median %.1f s.",
			first.Seconds(), write.Seconds()), "cache works"
	default:
		return fmt.Sprintf("Prompt cache: not working — reading the prompt took %.1f s the first time and still %.1f s after (median), so every question reads the instructions again. Writing: median %.1f s.",
			first.Seconds(), after.Seconds(), write.Seconds()), "no cache"
	}
}

// readyModel gets a model ready as the bot does — pulled when the server
// lacks it, thinking turned off — or says why it could not within wait.
func readyModel(ctx context.Context, out io.Writer, url, model string, wait time.Duration) (*reply.Ollama, error) {
	fmt.Fprintf(out, "%s: getting the model ready (a missing one is pulled first)…\n", model)
	o := reply.NewOllama(url, model)
	pctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	o.Prepare(pctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !o.Ready() {
		return nil, fmt.Errorf("%s: the model is not ready at %s after %s; is the server up, and the name right?", model, url, wait)
	}
	return o, nil
}

// timing is the median question and the first: the first also reads the
// fixed instructions, which a server that reuses its work does only once,
// so the gap between the two says whether it does.
func timing(took []time.Duration) string {
	if len(took) == 0 {
		return "no questions"
	}
	rest := slices.Clone(took[1:])
	if len(rest) == 0 {
		return fmt.Sprintf("%.1f s for the one question", took[0].Seconds())
	}
	slices.Sort(rest)
	return fmt.Sprintf("median %.1f s a question (the first %.1f s)", rest[len(rest)/2].Seconds(), took[0].Seconds())
}

// envOrDefault is a variable's value, or the default when it is unset or
// blank.
func envOrDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
