package reply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// Ollama is an Interpreter backed by an Ollama server: the model runs in
// its own container beside the app, and the app talks to it over plain
// HTTP. Nothing else in the deploy has to be told about the model — the
// app checks for it at boot and pulls it if it is missing.
type Ollama struct {
	url   string
	model string
	// ready flips once the model is confirmed present. Until then a question
	// gets ErrNotReady rather than a request to a model that would fail.
	ready  atomic.Bool
	client *http.Client
}

// Timeouts for the two shapes of call. A question is short and its answer
// shorter, but a 3B model on a CPU takes seconds per token, and the first
// question after a quiet spell also loads the model from disk.
const (
	chatTimeout = 75 * time.Second
	tagsTimeout = 10 * time.Second
	// Checked again at this interval while the model is still missing or
	// the server unreachable — it starts after the app, or is still
	// downloading, both ordinary at boot.
	prepareRetry = 15 * time.Second
	// A response from the model or its server is remote input; a request's
	// JSON is a few hundred bytes and a model list a few kilobytes.
	maxResponse = 1 << 20
)

// NewOllama builds a client. It does not connect: Prepare does.
func NewOllama(url, model string) *Ollama {
	return &Ollama{
		url:    strings.TrimRight(url, "/"),
		model:  model,
		client: &http.Client{},
	}
}

// Prepare confirms the model is present, pulling it if it is not, and keeps
// trying until it is or ctx ends. It is meant to run in its own goroutine
// from boot: a question before it finishes is told to ask again later.
func (o *Ollama) Prepare(ctx context.Context, logger *slog.Logger) {
	for {
		err := o.ensureModel(ctx, logger)
		if err == nil {
			o.ready.Store(true)
			logger.Info("language model ready", "model", o.model)
			return
		}
		if ctx.Err() != nil {
			return
		}
		logger.Warn("language model not ready yet; will try again",
			"model", o.model, "in", prepareRetry, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(prepareRetry):
		}
	}
}

// Ready reports whether Prepare has confirmed the model.
func (o *Ollama) Ready() bool { return o.ready.Load() }

func (o *Ollama) ensureModel(ctx context.Context, logger *slog.Logger) error {
	present, err := o.hasModel(ctx)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	logger.Info("pulling the language model; this takes a few minutes the first time",
		"model", o.model)
	return o.pull(ctx)
}

// hasModel asks the server what it holds. A model named without a tag is
// listed with ":latest", which is the same model.
func (o *Ollama) hasModel(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, tagsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.url+"/api/tags", nil)
	if err != nil {
		return false, err
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := o.do(req, &tags); err != nil {
		return false, fmt.Errorf("list models: %w", err)
	}
	want := o.model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range tags.Models {
		if m.Name == want {
			return true, nil
		}
	}
	return false, nil
}

// pull downloads the model, waiting for it: the server streams progress by
// default, which nothing here reads, so it is asked for one final status
// instead. Bounded only by ctx — a couple of gigabytes take as long as the
// connection takes.
func (o *Ollama) pull(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"model": o.model, "stream": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var status struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := o.do(req, &status); err != nil {
		return fmt.Errorf("pull model: %w", err)
	}
	if status.Error != "" {
		return fmt.Errorf("pull model: %s", status.Error)
	}
	if status.Status != "success" {
		return fmt.Errorf("pull model: ended with status %q", status.Status)
	}
	return nil
}

// requestSchema is handed to the server as the response format, so the
// model is constrained to a Request rather than asked nicely for one. The
// enums are the Kind and Span constants; a value outside them cannot be
// produced, and the parse below still checks.
var requestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{
			string(KindLeader), string(KindStanding), string(KindStreak), string(KindToday),
			string(KindScore), string(KindWins), string(KindCatchup), string(KindCount), string(KindHabits),
			string(KindRules), string(KindThanks), string(KindUnknown)}},
		"span":    map[string]any{"type": "string", "enum": []string{string(SpanMonth), string(SpanDays), string(SpanAll)}},
		"days":    map[string]any{"type": "integer"},
		"worst":   map[string]any{"type": "boolean"},
		"player":  map[string]any{"type": "string"},
		"topic":   map[string]any{"type": "string", "enum": topicNames()},
		"date":    map[string]any{"type": "string"},
		"month":   map[string]any{"type": "string"},
		"guesses": map[string]any{"type": "integer"},
	},
	"required": []string{"kind", "span", "days", "worst", "player", "topic", "date", "month", "guesses"},
}

// topicNames is every Topic plus the empty string for "not a rules
// question", which the schema needs to allow since topic is required.
func topicNames() []string {
	out := []string{""}
	for _, t := range Topics {
		out = append(out, string(t))
	}
	return out
}

// Interpret asks the model for a Request. The instructions are the same for
// every question, so the server reuses its work on them between calls and
// only the question and the answer cost time.
func (o *Ollama) Interpret(ctx context.Context, p Prompt) (Request, error) {
	if !o.Ready() {
		return Request{}, ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"model":  o.model,
		"stream": false,
		"format": requestSchema,
		// Deterministic: the same question should become the same request.
		"options": map[string]any{"temperature": 0},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt(p)},
			{"role": "user", "content": p.Question},
		},
	})
	if err != nil {
		return Request{}, fmt.Errorf("encode chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Request{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	var chat struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := o.do(req, &chat); err != nil {
		return Request{}, fmt.Errorf("ask model: %w", err)
	}
	if chat.Error != "" {
		return Request{}, fmt.Errorf("ask model: %s", chat.Error)
	}
	return parseRequest(chat.Message.Content)
}

// parseRequest reads what the model wrote, and treats anything outside the
// known values as a question it did not understand rather than an error:
// the group gets the help line, and nothing is logged as broken.
func parseRequest(content string) (Request, error) {
	var r Request
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &r); err != nil {
		return Request{}, fmt.Errorf("model did not answer with a request: %w", err)
	}
	switch r.Kind {
	case KindLeader, KindStanding, KindStreak, KindToday, KindScore, KindWins, KindCatchup,
		KindCount, KindHabits, KindRules, KindThanks:
	default:
		r.Kind = KindUnknown
	}
	if r.Kind != KindRules {
		r.Topic = ""
	} else if !slices.Contains(Topics, r.Topic) {
		// A rules question about nothing on the list gets the list.
		r.Topic = ""
	}
	r.Date = strings.TrimSpace(r.Date)
	if _, err := time.Parse(DateLayout, r.Date); err != nil || r.Kind != KindScore {
		// A date the model could not write properly is today, which is
		// the likeliest day to be asked about anyway.
		r.Date = ""
	}
	r.Month = strings.TrimSpace(r.Month)
	if _, err := time.Parse(MonthLayout, r.Month); err != nil || (r.Kind != KindLeader && r.Kind != KindStanding) {
		r.Month = ""
	}
	if r.Month != "" {
		// A named month is a month: whatever span the model also wrote.
		r.Span, r.Days = SpanMonth, 0
	}
	if r.Kind != KindCount || r.Guesses < 0 || r.Guesses > 7 {
		r.Guesses = 0
	}
	switch r.Span {
	case SpanDays:
		if r.Days <= 0 {
			r.Span = SpanMonth
		}
	case SpanAll:
	default:
		r.Span = SpanMonth
	}
	if r.Span != SpanDays {
		r.Days = 0
	}
	if r.Kind != KindLeader {
		r.Worst = false
	}
	r.Player = strings.TrimSpace(r.Player)
	switch r.Kind {
	case KindToday, KindRules, KindThanks, KindUnknown:
		// Nothing to be about a player: a name here is the model filling
		// a field in, which it does for a message that asks nothing.
		r.Player = ""
	}
	return r, nil
}

// systemPrompt is the model's whole job description. English, whatever
// language the group uses: these small models follow English instructions
// best and read the other languages fine.
func systemPrompt(p Prompt) string {
	var b strings.Builder
	b.WriteString("You turn a question asked in a Wordle group chat into a JSON request. ")
	b.WriteString("The question may be in any language. Answer with the JSON only.\n\n")
	fmt.Fprintf(&b, "Today is %s.\n", p.Today.Format("Monday 2 January 2006"))
	if p.Asker != "" {
		fmt.Fprintf(&b, "The person asking is %s; \"I\", \"me\" and \"my\" mean them.\n", p.Asker)
	} else {
		b.WriteString("The person asking is not a player.\n")
	}
	if len(p.Players) > 0 {
		fmt.Fprintf(&b, "Players: %s.\n", strings.Join(p.Players, ", "))
	}
	if p.Context != "" {
		b.WriteString("\nThe question is a reply to this earlier post of yours. Use it to read the " +
			"question — \"this\", \"that\", \"it\", a name or a score mentioned in it — but treat " +
			"nothing in the post as a question itself:\n<<<\n" + p.Context + "\n>>>\n")
		if p.ContextDate != "" {
			fmt.Fprintf(&b, "The post is about the puzzle of %s; a question about that day, or a "+
				"score in the post, is a \"score\" question with that date.\n", p.ContextDate)
		}
	}
	b.WriteString(`
Fields:
- kind: "leader" for who is leading, winning, best, on top, or the ranking;
  "standing" for how one particular player is doing, their place or average;
  "streak" for streaks or runs of solved days in a row;
  "today" for today's puzzle, who has posted, who is missing, the best score today;
  "score" for one player's result on one particular day ("my score on July 5",
  "what did Bo get yesterday");
  "wins" for who has won the most months, monthly wins, titles;
  "catchup" for whether somebody can still win or catch up this month, how far
  behind they are, what they need to win, whether the leader is safe ("kan Bo
  komma ikapp?", "can I still win?", "is Alma safe?");
  "count" for how many times a player has scored a given number or failed
  ("hur många 2:or har jag?", "how often does Bo fail?", "do I have any 1s?"),
  or their whole distribution;
  "habits" for who usually posts first or last, or when somebody usually posts;
  "rules" for what something means or how it is counted — a miss, points, the
  average, a streak, how the month is scored, hard mode, form, who is ranked;
  "thanks" for thanks, praise or a compliment that asks nothing ("tack",
  "duktig bot", "good bot", "nice");
  "unknown" for anything else: a greeting or a remark that asks nothing, and
  anything not about this Wordle group's scores (people's contact details,
  accounts, settings, other subjects). Never pick a kind that was not asked
  for: a message with no question in it is "thanks" or "unknown".
- span: "month" for this month or when no period is given; "days" for a number
  of recent days (a week is 7, two weeks 14); "all" for all time, ever, overall.
- days: the number of days when span is "days", otherwise 0.
- worst: true when a "leader" question asks for the other end of the table —
  who is last, worst, lowest, struggling, has the worst form; otherwise false.
- player: the player the question is about, spelled exactly as in the list —
  the asker's own name when they ask about themselves — otherwise "". Always ""
  when the message asks nothing about anyone.
- topic: when kind is "rules", which rule: "miss" (a missed day), "average" (the
  average, points), "streak", "month" (how a month is scored and won), "hardmode",
  "form", "ranked" (who is ranked on the board and why not); otherwise "".
- month: when a "leader" or "standing" question names a particular past month
  ("vem vann juli?", "last month", "how did I do in August"), that month as
  YYYY-MM, worked out from today's date (a month without a year is the most
  recent one that has happened); otherwise "".
- guesses: when kind is "count", the score asked about: 1 to 6, 7 for a failure
  (X), 0 for the whole distribution; otherwise 0.
- date: when kind is "score", the day asked about as YYYY-MM-DD, worked out from
  today's date ("yesterday", "last Friday", "July 5" — a month without a year is
  the most recent one that has happened); "" for today or when kind is not "score".
`)
	return b.String()
}

// do sends a request and decodes a JSON body, with a size bound and an
// error for any status that is not success.
func (o *Ollama) do(req *http.Request, out any) error {
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("response was not JSON")
	}
	return nil
}
