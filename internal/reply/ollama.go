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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/martinstenrose/wordleland/internal/wordle"
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
	// thinks is set when the server says the model has a thinking mode
	// (qwen3 and others), which every chat then turns off: it spends tens
	// of seconds on a CPU reasoning before a one-line answer, and a chat
	// reply is not worth the wait. Only for a model that says so, since an
	// older server may refuse the setting for one that does not.
	thinks atomic.Bool

	// OnUsage, when set, is told what the server reports of each placing
	// call. The placing test sets it to see whether the server reuses its
	// work on the instructions; the bot leaves it nil. Set before the
	// first call and not after.
	OnUsage func(Usage)
}

// Usage is what the server reports of one call: how long it took to read
// the prompt, and to write the answer. Reading is what a server that
// reuses its work on a prompt's unchanged start saves.
type Usage struct {
	Reading, Writing time.Duration
}

// Timeouts for the two shapes of call. A question is short and its answer
// shorter, but a 3B model on a CPU takes seconds per token, and the first
// question after a quiet spell also loads the model from disk.
const (
	chatTimeout = 75 * time.Second
	tagsTimeout = 10 * time.Second
	// warmTimeout bounds loading the model from disk ahead of time: a few
	// gigabytes off a slow disk.
	warmTimeout = 5 * time.Minute
	// Checked again at this interval while the model is still missing or
	// the server unreachable — it starts after the app, or is still
	// downloading, both ordinary at boot.
	prepareRetry = 15 * time.Second
	// contextSize is the context every call asks for, in tokens. Set, not
	// left to the server: its default on a CPU is 4096, and a prompt past
	// it is cut from the front — the instructions first — with a 200 and
	// only a warning in the server's log. The placing instructions alone
	// are about 1.2k tokens. The same number on every call, the warm-up
	// included: the server reloads the model when a request asks for
	// another.
	contextSize = 8192
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
			o.checkThinking(ctx)
		}
		if err == nil {
			o.ready.Store(true)
			logger.Info("language model ready", "model", o.model)
			o.warm(ctx, logger)
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

// warm loads the model into memory now rather than at the first question,
// which on a CPU would otherwise wait tens of seconds for the disk before
// the model even starts. The server keeps it loaded after (compose.yml
// sets OLLAMA_KEEP_ALIVE). Best effort: the model is ready either way, and
// a question meanwhile simply waits its turn at the server.
func (o *Ollama) warm(ctx context.Context, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	// A generate request with no prompt only loads the model — with the
	// context the questions will ask for, or the first one reloads it.
	body, _ := json.Marshal(map[string]any{"model": o.model,
		"options": map[string]any{"num_ctx": contextSize}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Error string `json:"error"`
	}
	if err := o.do(req, &out); err != nil || out.Error != "" {
		logger.Info("could not load the language model ahead of the first question",
			"model", o.model, "error", err, "server_error", out.Error)
	}
}

// checkThinking asks the server whether the model has a thinking mode,
// which every chat then turns off. A server too old to say, or that cannot
// be asked, is no reason to hold the model back: it is used as it is.
func (o *Ollama) checkThinking(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, tagsTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": o.model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/api/show", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := o.do(req, &show); err != nil {
		return
	}
	o.thinks.Store(slices.Contains(show.Capabilities, "thinking"))
}

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

// noThinking turns a thinking model's thinking off for one chat request.
func (o *Ollama) noThinking(body map[string]any) map[string]any {
	if o.thinks.Load() {
		body["think"] = false
	}
	return body
}

// thinkBlock is reasoning a model wrote into its answer anyway, as some
// server versions pass it through: never the answer, never posted.
var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// withoutThinking is a model's answer without any reasoning in it.
func withoutThinking(content string) string {
	return strings.TrimSpace(thinkBlock.ReplaceAllString(content, ""))
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
var requestSchema = func() map[string]any {
	one := map[string]any{
		"kind":     map[string]any{"type": "string", "enum": enum(Kinds)},
		"span":     map[string]any{"type": "string", "enum": enum(Spans)},
		"days":     map[string]any{"type": "integer"},
		"worst":    map[string]any{"type": "boolean"},
		"player":   map[string]any{"type": "string"},
		"other":    map[string]any{"type": "string"},
		"topic":    map[string]any{"type": "string", "enum": topicNames()},
		"date":     map[string]any{"type": "string"},
		"puzzle":   map[string]any{"type": "integer"},
		"month":    map[string]any{"type": "string"},
		"guesses":  map[string]any{"type": "integer"},
		"orbetter": map[string]any{"type": "boolean"},
		"scores": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"player":  map[string]any{"type": "string"},
				"guesses": map[string]any{"type": "integer"},
			},
			"required": []string{"player", "guesses"},
		}},
	}
	required := []string{"kind", "span", "days", "worst", "player", "other", "topic", "date", "puzzle",
		"month", "guesses", "orbetter", "scores"}
	// The further questions of a message that asks more than one: each a
	// request of the same shape, one level deep.
	top := map[string]any{"also": map[string]any{
		"type":     "array",
		"maxItems": maxAlso,
		"items":    map[string]any{"type": "object", "properties": one, "required": required},
	}}
	for k, v := range one {
		top[k] = v
	}
	return map[string]any{
		"type":       "object",
		"properties": top,
		"required":   append(slices.Clone(required), "also"),
	}
}()

// enum is a list of constants as the schema's strings.
func enum[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
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

// Interpret asks the model for a Request. The instructions start the same
// for every question, so the server reuses its work on them between calls
// and only what follows them costs time; see systemPrompt.
func (o *Ollama) Interpret(ctx context.Context, p Prompt) (Request, error) {
	if !o.Ready() {
		return Request{}, ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	body, err := json.Marshal(o.noThinking(map[string]any{
		"model":  o.model,
		"stream": false,
		"format": requestSchema,
		// Deterministic: the same question should become the same request.
		// No presence penalty, whatever the model ships with (qwen3.5:
		// 1.5): a request repeats its quotes and field names by design.
		"options": map[string]any{"temperature": 0, "presence_penalty": 0, "num_ctx": contextSize},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt(p)},
			{"role": "user", "content": p.Question},
		},
	}))
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
		// Nanoseconds, as the server reports them.
		PromptEvalDuration int64 `json:"prompt_eval_duration"`
		EvalDuration       int64 `json:"eval_duration"`
	}
	if err := o.do(req, &chat); err != nil {
		return Request{}, fmt.Errorf("ask model: %w", err)
	}
	if chat.Error != "" {
		return Request{}, fmt.Errorf("ask model: %s", chat.Error)
	}
	if o.OnUsage != nil {
		o.OnUsage(Usage{Reading: time.Duration(chat.PromptEvalDuration), Writing: time.Duration(chat.EvalDuration)})
	}
	return parseRequest(withoutThinking(chat.Message.Content))
}

// maxSpanDays is the longest "last N days" that is still a span rather
// than all time. A year; beyond it the board's own table is the answer.
const maxSpanDays = 366

// maxAlso is how many further questions one message may carry: three
// answers in one post is already a lot of chat.
const maxAlso = 2

// parseRequest reads what the model wrote, and treats anything outside the
// known values as a question it did not understand rather than an error:
// the group gets the help line, and nothing is logged as broken.
func parseRequest(content string) (Request, error) {
	var r Request
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &r); err != nil {
		return Request{}, fmt.Errorf("model did not answer with a request: %w", err)
	}
	also := r.Also
	r = normalise(r)
	for _, a := range also {
		a = normalise(a)
		switch a.Kind {
		case KindUnknown, KindHelp, KindThanks:
			// Not a further question: a greeting or a thank-you alongside
			// the real one says nothing an answer could.
			continue
		}
		duplicate := reflect.DeepEqual(a, r)
		for _, kept := range r.Also {
			duplicate = duplicate || reflect.DeepEqual(a, kept)
		}
		if !duplicate && len(r.Also) < maxAlso {
			r.Also = append(r.Also, a)
		}
	}
	switch r.Kind {
	case KindUnknown, KindHelp, KindThanks:
		// "Tack! Och vem leder?": the question is what gets answered.
		if len(r.Also) > 0 {
			rest := r.Also[1:]
			r = r.Also[0]
			if len(rest) > 0 {
				r.Also = rest
			}
		}
	}
	return r, nil
}

// normalise keeps what a request says only where it means something, one
// request at a time; further questions are the caller's.
func normalise(r Request) Request {
	r.Also = nil
	if !slices.Contains(Kinds, r.Kind) {
		r.Kind = KindUnknown
	}
	if r.Kind != KindRules {
		r.Topic = ""
	} else if !slices.Contains(Topics, r.Topic) {
		// A rules question about nothing on the list gets the list.
		r.Topic = ""
	}
	dated := r.Kind == KindScore || r.Kind == KindDay || r.Kind == KindWhatIf
	r.Date = strings.TrimSpace(r.Date)
	if _, err := time.Parse(DateLayout, r.Date); err != nil || !dated {
		// A date the model could not write properly is the default day,
		// which is the likeliest day to be asked about anyway.
		r.Date = ""
	}
	if r.Kind != KindScore && r.Kind != KindDay {
		r.Puzzle = 0
	}
	if r.Puzzle != 0 {
		// A puzzle number is a date by arithmetic, done here: "Wordle
		// 1 900" is exact, and the model would only guess its date.
		if date, err := wordle.DateForPuzzle(r.Puzzle); err == nil {
			r.Date = date.Format(DateLayout)
		}
		r.Puzzle = 0
	}
	r.Month = strings.TrimSpace(r.Month)
	if _, err := time.Parse(MonthLayout, r.Month); err != nil || !slices.Contains(spanned, r.Kind) {
		r.Month = ""
	}
	if r.Month != "" {
		// A named month is a month: whatever span the model also wrote.
		r.Span, r.Days = SpanMonth, 0
	}
	if r.Kind != KindCount || r.Guesses < 0 || r.Guesses > 7 {
		r.Guesses = 0
	}
	if r.Guesses < 1 || r.Guesses > 6 {
		// "X or better" is every game, which is not a question.
		r.OrBetter = false
	}
	switch r.Span {
	case SpanDays:
		if r.Days <= 0 {
			r.Span = SpanMonth
		}
		if r.Days > maxSpanDays {
			// "The last billion days" is all time, and scoring it day by
			// day would be work the answer's deadline cannot interrupt.
			r.Span, r.Days = SpanAll, 0
		}
	case SpanAll, SpanWeek, SpanLastWeek:
	default:
		r.Span = SpanMonth
	}
	if r.Span != SpanDays {
		r.Days = 0
	}
	switch r.Kind {
	case KindLeader, KindPuzzles, KindForm, KindCount:
	default:
		r.Worst = false
	}
	r.Player = pronoun(strings.TrimSpace(r.Player))
	switch r.Kind {
	case KindLeader, KindToday, KindRules, KindThanks, KindHelp, KindUnknown,
		KindWhatIf, KindDay, KindPuzzles, KindRecords, KindGroup:
		// Nothing to be about a player: a name here is the model filling
		// a field in, which it does for a message that asks nothing.
		r.Player = ""
	}
	if r.Player == Group && r.Kind != KindCount {
		r.Player = ""
	}
	r.Other = pronoun(strings.TrimSpace(r.Other))
	if r.Other == Group {
		r.Other = ""
	}
	if r.Kind != KindVersus || strings.EqualFold(r.Other, r.Player) {
		r.Other = ""
	}
	if r.Kind != KindWhatIf {
		r.Scores = nil
	}
	var scores []Hypothetical
	seen := map[string]bool{}
	for _, h := range r.Scores {
		h.Player = pronoun(strings.TrimSpace(h.Player))
		// One result per player: a day has one, and a second is the
		// model repeating itself. The first is the one asked about.
		key := strings.ToLower(h.Player)
		if h.Player != "" && !seen[key] && h.Guesses >= 1 && h.Guesses <= failGuesses && len(scores) < maxHypotheticals {
			scores = append(scores, h)
			seen[key] = true
		}
	}
	r.Scores = scores
	return r
}

// The model writes a pronoun where a name should be often enough — "me"
// for "how many 2s do I have", "vi" for the group — that it is read here
// rather than answered with "I don't know me". The asker's are read as
// Asker and filled in with their name once known; the group's only mean
// something to a count, and elsewhere name nobody.
const (
	Asker = "me"
	Group = "group"
)

var (
	askerWords = []string{"me", "i", "myself", "jag", "mig", "mej", "själv"}
	groupWords = []string{"group", "we", "us", "everyone", "everybody", "vi", "oss", "gruppen", "alla"}
)

// pronoun is a player field with a pronoun read as Asker or Group, and any
// other name as it was.
func pronoun(name string) string {
	switch lower := strings.ToLower(name); {
	case slices.Contains(askerWords, lower):
		return Asker
	case slices.Contains(groupWords, lower):
		return Group
	}
	return name
}

// spanned are the kinds a span, and a named month, apply to.
var spanned = []Kind{KindLeader, KindStanding, KindVersus, KindPuzzles, KindDayWins, KindCatchup, KindWins}

// maxHypotheticals bounds a what-if question: more results than players
// is the model inventing them.
const maxHypotheticals = 20

// systemPrompt is the model's whole job description. English, whatever
// language the group uses: these small models follow English instructions
// best and read the other languages fine.
func systemPrompt(p Prompt) string {
	var b strings.Builder
	b.WriteString("You turn a question asked in a Wordle group chat into a JSON request. ")
	b.WriteString("The question may be in any language. Answer with the JSON only.\n\n")
	b.WriteString(`
Fields:
- kind: "leader" for who is leading, winning, best, on top, or the ranking ("vem
  ligger etta?", "vem toppar?"), and
  who won a past month ("vem vann juni?", "vem vann förra månaden?", with
  month set);
  "standing" for how one particular player is doing, their place or average
  ("hur ligger jag till?", "var ligger Bo?") —
  or, with no player, the whole table: everyone's standing, "ställningen",
  "the standings", "how is everyone doing";
  "streak" for streaks or runs of solved days in a row ("svit", "i rad": "vem
  har flest dagar i rad?", "hur lång är min svit?") — not a count of scores;
  "today" for today's puzzle, who has posted, who is missing or left to post,
  the best score today ("vem är kvar att svara idag?", "vilka har inte spelat
  än?", "vem saknas?", "har alla postat?");
  "score" for one player's result on one particular day ("my score on July 5",
  "what did Bo get yesterday", "vad fick Bo i onsdags?");
  "day" for everyone's results on one day, and how hard that puzzle was ("how
  did everyone do yesterday?", "vad fick alla igår?", "was today's hard?");
  "wins" for counting titles: who has won the most months, how many months a
  player has won ("hur många månader har Bo vunnit?") — not who won or wins a
  particular month, and never the asker unless they ask about themselves;
  "catchup" for whether somebody can still win or catch up this month, how far
  behind they are, what they need to win, whether the leader is safe ("kan Bo
  komma ikapp?", "can I still win?", "is Alma safe?"), and who will win the
  month ("vem vinner månaden?", "vem vinner september?", with month set when
  one is named) — not who is left to post today, which is "today";
  "whatif" for what would happen if somebody got a particular score: "if Bo
  gets a 6 tomorrow and Alma a 3, who leads?", "om jag får en 2:a idag?";
  "count" for how many times a player has scored a given number or failed
  ("hur många 2:or har jag?", "how often does Bo fail?", "do I have any 1s?"),
  who has the most or fewest of a score, a player's whole distribution, or the
  whole group's total ("hur många 2:or har vi?", player "group") — never a
  streak ("svit", "i rad"), which is "streak";
  "versus" for two players compared, head to head ("how do I stand against
  Bo?", "vem är bäst av Alma och Bo?", "Alma vs Bo");
  "daywins" for who most often has the best score of the day, days won ("vem
  har vunnit flest dagar?");
  "form" for who is in form, hot, improving, in a slump, playing well lately
  ("vem spelar bäst just nu?", "vem är het?");
  "steady" for who is most consistent, steady, reliable, or unpredictable ("vem
  är jämnast?");
  "puzzles" for the hardest or easiest puzzle or day ("vilket var det svåraste
  ordet i augusti?");
  "weekday" for which day of the week is hardest or best, for the group or a
  player ("är söndagar svårast?");
  "profile" for everything about one player: "tell me about Bo", "berätta om
  mig", "roast Alma", "what do you know about me?";
  "history" for one player's months, month by month, their best month ("vilken
  var min bästa månad?");
  "records" for the group's records, all-time bests, "rekorden";
  "group" for the group as a whole: how many play, how many games, the group's
  average ("hur många spelare är vi?");
  "habits" for who usually posts first or last, or when somebody usually posts
  ("vem postar sist?");
  "rules" for what something means or how it is counted — a miss, points, the
  average, a streak, how the month is scored, hard mode, form, who is ranked —
  or what the bot knows (the words, a starting word) ("vad betyder punkter?");
  "help" for asking what the bot can do, how to use it, or which questions it
  answers ("what can you do?", "vad kan du?", "help", "hjälp");
  "thanks" for thanks, praise or a compliment that asks nothing ("tack",
  "duktig bot", "good bot", "nice");
  "unknown" for anything else: a greeting or a remark that asks nothing, and
  anything not about this Wordle group's scores (people's contact details,
  accounts, settings, other subjects). Never pick a kind that was not asked
  for: a message with no question in it is "thanks" or "unknown".
- span: "month" for this month, and for who is leading or winning when no
  period is given (a month is the competition being led); "all" for all time,
  ever, overall, and — when no period is given — for who is best, the best
  player, the best average, and for any "standing" question, one player's or
  the whole table ("vem är bäst?", "ställningarna", "how is Bo doing?" are all
  time: the board); "week" for this week, "lastweek" for last week (Monday to
  Sunday); "days" for a number of recent days (two weeks is 14).
- days: the number of days when span is "days", otherwise 0.
- worst: true for the other end — for "leader", who is last, worst, lowest,
  struggling; for "form", who is in the worst form or slipping; for "puzzles",
  the easiest rather than the hardest; for "count", the fewest rather than the
  most. Otherwise false.
- player: the player the question is about, spelled exactly as in the list —
  the asker's own name when they ask about themselves — otherwise "". Always ""
  when the message asks nothing about anyone.
  A question that asks who ("vem", "who", "vilka") is about the whole group:
  player is "" whoever is asking ("vem har bäst form?", "vem vann juni?") —
  unless it names players itself ("vem är bäst av Alma och Bo?").
- other: when kind is "versus", the second player; "" when the asker compares
  themselves with the player in "player".
- topic: when kind is "rules", which rule: "miss" (a missed day), "average" (the
  average, points), "streak", "month" (how a month is scored and won), "hardmode",
  "form", "ranked" (who is ranked on the board and why not), "data" (what the bot
  knows: the words, anyone's starting word); otherwise "".
- month: when a question names a particular past month ("vem vann juli?", "last
  month", "how did I do in August"), that month as YYYY-MM, worked out from
  today's date (a month without a year is the most recent one that has
  happened); otherwise "".
- guesses: when kind is "count", the score asked about: 1 to 6, 7 for a failure
  (X), 0 for the whole distribution ("2:or", "tvåor", "en 2:a" are 2; "X",
  "missar", "fails" are 7); otherwise 0.
- orbetter: true when a "count" question asks for that score or better ("3 or
  better", "3 eller bättre"); otherwise false.
- date: when kind is "score", "day" or "whatif", the day asked about as
  YYYY-MM-DD, worked out from today's date ("yesterday", "tomorrow", "last
  Friday", "July 5" — a month without a year is the most recent one that has
  happened); "" for the usual day (today) or when the kind takes no date.
- puzzle: when a "score" or "day" question names a puzzle by its number
  ("Wordle 1900"), that number; otherwise 0.
- scores: when kind is "whatif", each made-up result as {"player", "guesses"},
  guesses 1 to 6 or 7 for an X ("om jag får en 6:a" is the asker, 6); otherwise [].
- also: when the message asks more than one thing ("vem leder, och har jag
  svit?", "how did I do yesterday and who is in form?"), the first question
  goes in the fields above and each further one here as its own request with
  the same fields, at most 2. Otherwise [].
`)
	// Last, and in this order, what changes: today and the players once a
	// day at most, the asker, the question before and the quoted post with
	// every question. The
	// server reuses its work on a prompt only up to the first difference,
	// so everything above is read once, not once per question.
	b.WriteString("\n")
	fmt.Fprintf(&b, "Today is %s.\n", p.Today.Format("Monday 2 January 2006"))
	if len(p.Players) > 0 {
		fmt.Fprintf(&b, "Players: %s.\n", strings.Join(p.Players, ", "))
	}
	if p.Asker != "" {
		fmt.Fprintf(&b, "The person asking is %s; \"I\", \"me\" and \"my\" mean them.\n", p.Asker)
	} else {
		b.WriteString("The person asking is not a player.\n")
	}
	if p.Previous != nil {
		fmt.Fprintf(&b, "\nThe group's last question, a few minutes ago, became this request: %s\n"+
			"When this message only makes sense as a follow-up to it — \"and Bo?\", \"och jag då?\", "+
			"\"och förra månaden?\", \"what about all time?\", \"och sämst?\", or a bare name saying "+
			"who was meant — answer with that request, changing only what the message changes. A "+
			"message that asks a whole question of its own ignores it.\n", Describe(*p.Previous))
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
