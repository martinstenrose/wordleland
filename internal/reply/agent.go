package reply

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Agent answers the questions the Interpreter could not place, by letting
// a larger model look things up and write the answer itself.
//
// The lookups are the answers the bot already gives — each Kind is a tool,
// rendered by answer from internal/stats exactly as a placed question is —
// plus one that lists a player's results day by day. The model may call
// several, and combine them: "who has the most 2s, and is their streak
// still going?" is two lookups and one sentence.
//
// The model writes the sentence, so it could write a number of its own.
// It is not trusted to: every number in its answer must appear in what the
// lookups returned, the question, or the instructions it was given. An
// answer that fails the check is replaced by the lookups themselves, which
// are the bot's own words and figures. What the model is trusted with is
// which lookups to make and how to phrase what they said.
type Agent struct {
	*Ollama
}

// NewAgent builds the agent's client. Like NewOllama it does not connect:
// Prepare does, and pulls the model if it is missing.
func NewAgent(url, model string) *Agent {
	return &Agent{Ollama: NewOllama(url, model)}
}

// Bounds on one answer. A question worth a lookup is rarely worth more than
// two; the rest is room for a model that looks something up it did not
// need to. Past them, the lookups made so far are the answer.
const (
	maxAgentRounds  = 4
	maxCallsInRound = 4
	// A day-by-day list longer than two months is a table, and the board
	// is where a table is read.
	maxResultsDays = 62
	// agentSendReserve is kept back from the answer's deadline for posting
	// it.
	agentSendReserve = 15 * time.Second
)

// errNoLookup is a model that answered without looking anything up: a
// greeting, or a question about something other than this group's Wordle.
// Run returns what it said with it, and offTopic decides whether that may
// be posted.
var errNoLookup = errors.New("the model looked nothing up")

// chatMessage is one message of an Ollama chat, in both directions.
type chatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	// ToolName says which call a tool message answers.
	ToolName string `json:"tool_name,omitempty"`
}

type toolCall struct {
	Function struct {
		Name string `json:"name"`
		// An object as Ollama sends it; json.RawMessage so an older
		// server's string-encoded object reads too. See callArguments.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// tool is one lookup the model is offered: a Kind answered by answer, or
// the results list, and which of the Request's fields it takes.
type tool struct {
	name        string
	description string
	params      []string
}

// tools is what the model can look up. The descriptions are the model's
// only guide to which to call, so they say what comes back, not how.
var tools = []tool{
	{string(KindLeader), "Who leads a span (this month by default, a past month, the last N days, or all time), with their average; worst=true for the bottom of the table.",
		[]string{"span", "days", "month", "worst"}},
	{string(KindStanding), "One player's place and average over a span; with no player, the whole ranked table for the span.",
		[]string{"player", "span", "days", "month"}},
	{string(KindStreak), "A player's current and longest streak of solved days; with no player, who holds the current and the longest streak.",
		[]string{"player"}},
	{string(KindToday), "Today's puzzle: how many have posted, the best score so far, who is still missing.",
		nil},
	{string(KindScore), "One player's result on one day.",
		[]string{"player", "date"}},
	{string(KindWins), "How many months a player has won; with no player, who has won the most months.",
		[]string{"player"}},
	{string(KindCatchup), "Whether a player can still win this month: the gap to the leader, the days left, what it would take.",
		[]string{"player"}},
	{string(KindCount), "How many times a player has scored a number of guesses (1-6, 7 for a failure), or with guesses 0 their whole distribution.",
		[]string{"player", "guesses"}},
	{string(KindHabits), "When a player usually posts and how often they post first or last; with no player, who usually posts first and last.",
		[]string{"player"}},
	{string(KindRules), "How the group's scoring works, one topic at a time.",
		[]string{"topic"}},
	{toolResults, "A player's result on each day from one date to another, at most 62 days: the number of guesses, X for a failure, - for a day not played, * for hard mode.",
		[]string{"player", "from", "to"}},
}

const toolResults = "results"

// toolParams describes each parameter a tool can take. The Request's own,
// as the Interpreter's schema has them, and the results list's two dates.
var toolParams = map[string]map[string]any{
	"span":    {"type": "string", "enum": []string{string(SpanMonth), string(SpanDays), string(SpanAll)}, "description": "month, days (the last N days) or all (all time)"},
	"days":    {"type": "integer", "description": "the number of days when span is days"},
	"month":   {"type": "string", "description": "a past month as YYYY-MM"},
	"worst":   {"type": "boolean", "description": "true for the bottom of the table"},
	"player":  {"type": "string", "description": "a player's name exactly as in the list, or empty"},
	"date":    {"type": "string", "description": "a day as YYYY-MM-DD, empty for today"},
	"guesses": {"type": "integer", "description": "1 to 6, 7 for a failure, 0 for the whole distribution"},
	"topic":   {"type": "string", "enum": topicNames()},
	"from":    {"type": "string", "description": "the first day as YYYY-MM-DD"},
	"to":      {"type": "string", "description": "the last day as YYYY-MM-DD, empty for today"},
}

// toolDefinitions is tools in the shape Ollama's chat takes. No parameter
// is required: an empty one means what it means in a Request.
func toolDefinitions() []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tl := range tools {
		props := map[string]any{}
		for _, p := range tl.params {
			props[p] = toolParams[p]
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tl.name,
				"description": tl.description,
				"parameters":  map[string]any{"type": "object", "properties": props},
			},
		})
	}
	return out
}

// lookup answers one tool call from the history. Each Kind goes through
// parseRequest, so a call is held to the same rules a placed question is —
// a field the Kind does not use is dropped there — and then through
// answer, so it reads as the bot's own reply would.
func lookup(t i18n.Translator, name string, args json.RawMessage, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) (string, error) {

	fields := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return "", fmt.Errorf("arguments are not an object: %w", err)
		}
	}
	if !slices.ContainsFunc(tools, func(tl tool) bool { return tl.name == name }) {
		return "", fmt.Errorf("no such tool %q", name)
	}
	if name == toolResults {
		return resultsList(t, fields, asker, players, results, now)
	}
	fields["kind"] = name
	raw, _ := json.Marshal(fields)
	req, err := parseRequest(string(raw))
	if err != nil {
		return "", err
	}
	return answer(t, req, asker, players, results, now), nil
}

// resultsList is a player's results day by day, for questions the answers
// do not cover: "what did Bo get this week?", "did I play on Sunday?".
func resultsList(t i18n.Translator, fields map[string]any, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) (string, error) {

	player, _ := fields["player"].(string)
	p, ok, text := whom(t, Request{Player: strings.TrimSpace(player)}, asker, players)
	if !ok {
		return text, nil
	}
	day := func(key string, fallback time.Time) (time.Time, error) {
		s, _ := fields[key].(string)
		if s = strings.TrimSpace(s); s == "" {
			return fallback, nil
		}
		d, err := time.ParseInLocation(DateLayout, s, now.Location())
		if err != nil {
			return time.Time{}, fmt.Errorf("%s is not YYYY-MM-DD", key)
		}
		return d, nil
	}
	to, err := day("to", now)
	if err != nil {
		return "", err
	}
	from, err := day("from", to.AddDate(0, 0, -6))
	if err != nil {
		return "", err
	}
	first, last := wordle.PuzzleForDate(from), wordle.PuzzleForDate(to)
	last = min(last, wordle.PuzzleForDate(now))
	if first > last {
		return "", errors.New("from is after to")
	}
	if last-first >= maxResultsDays {
		first = last - maxResultsDays + 1
	}

	got := map[int]store.BoardResult{}
	for _, r := range results {
		if r.PlayerID == p.ID && r.PuzzleNo >= first && r.PuzzleNo <= last {
			got[r.PuzzleNo] = r
		}
	}
	lines := []string{p.Name + ":"}
	for n := first; n <= last; n++ {
		date, err := wordle.DateForPuzzle(n)
		if err != nil {
			continue
		}
		mark := "-"
		if r, ok := got[n]; ok {
			switch {
			case !r.Solved:
				mark = "X"
			default:
				mark = strconv.Itoa(r.Guesses)
			}
			if r.HardMode {
				mark += "*"
			}
		}
		lines = append(lines, date.Format(DateLayout)+" "+date.Weekday().String()+": "+mark)
	}
	return strings.Join(lines, "\n"), nil
}

// callArguments reads a call's arguments whether the server sent them as an
// object or as a string holding one.
func callArguments(raw json.RawMessage) json.RawMessage {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return json.RawMessage(s)
	}
	return raw
}

// Run lets the model look things up until it answers, and reports the
// answer with every lookup's text, which is what the answer is checked
// against and what replaces it when the check fails. errNoLookup, with
// what the model said, when it answered without looking anything up.
func (a *Agent) Run(ctx context.Context, p Prompt,
	look func(name string, args json.RawMessage) (string, error)) (string, []string, error) {

	if !a.Ready() {
		return "", nil, ErrNotReady
	}
	messages := []chatMessage{
		{Role: "system", Content: agentPrompt(p)},
		{Role: "user", Content: p.Question},
	}
	var looked []string
	for range maxAgentRounds {
		reply, err := a.chat(ctx, messages)
		if err != nil {
			return "", looked, err
		}
		if len(reply.ToolCalls) == 0 {
			if len(looked) == 0 {
				return strings.TrimSpace(reply.Content), nil, errNoLookup
			}
			return strings.TrimSpace(reply.Content), looked, nil
		}
		calls := reply.ToolCalls
		if len(calls) > maxCallsInRound {
			calls = calls[:maxCallsInRound]
		}
		messages = append(messages, chatMessage{Role: "assistant", Content: reply.Content, ToolCalls: calls})
		for _, c := range calls {
			text, err := look(c.Function.Name, callArguments(c.Function.Arguments))
			if err != nil {
				// Told to the model, which can call again properly.
				text = "Error: " + err.Error()
			} else {
				looked = append(looked, text)
			}
			messages = append(messages, chatMessage{Role: "tool", Content: text, ToolName: c.Function.Name})
		}
	}
	// Out of rounds with lookups in hand: those are the answer.
	if len(looked) == 0 {
		return "", nil, errNoLookup
	}
	return "", looked, nil
}

// chat is one round: the conversation so far, the tools, and the model's
// next message. Bounded by ctx alone — the bridge's deadline for the whole
// answer — since how long a round takes depends on how much it looked up.
func (a *Agent) chat(ctx context.Context, messages []chatMessage) (chatMessage, error) {
	body, err := json.Marshal(map[string]any{
		"model":  a.model,
		"stream": false,
		"tools":  toolDefinitions(),
		// Not zero, as the placing model's is: the same question may
		// get a different quip, and the numbers are checked either way.
		"options":  map[string]any{"temperature": 0.6},
		"messages": messages,
	})
	if err != nil {
		return chatMessage{}, fmt.Errorf("encode chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return chatMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Message chatMessage `json:"message"`
		Error   string      `json:"error"`
	}
	if err := a.do(req, &out); err != nil {
		return chatMessage{}, fmt.Errorf("ask model: %w", err)
	}
	if out.Error != "" {
		return chatMessage{}, fmt.Errorf("ask model: %s", out.Error)
	}
	return out.Message, nil
}

// agentPrompt is the agent's job description. English for the same reason
// as systemPrompt's; the answer is asked for in the question's language.
func agentPrompt(p Prompt) string {
	var b strings.Builder
	b.WriteString("You are the bot in a Wordle group chat. Answer the question using the tools. ")
	b.WriteString("Every number, score, date and name-to-result you state must come from a tool result " +
		"in this conversation; never work out a figure yourself. If the tools cannot answer it, say " +
		"so in one sentence. A question about this group's players, scores or standings is always " +
		"answered from the tools, never from memory. Anything else you may answer briefly from what " +
		"you know, without any numbers; if you are not sure, say you don't know.\n")
	b.WriteString("Reply in the language the question is asked in, in one to three short sentences " +
		"of plain text, no markdown.\n")
	b.WriteString(persona)
	b.WriteString("A lower average is better. A failed puzzle counts as 7.\n\n")
	fmt.Fprintf(&b, "Today is %s (%s).\n", p.Today.Format("Monday 2 January 2006"), p.Today.Format(DateLayout))
	if p.Asker != "" {
		fmt.Fprintf(&b, "The person asking is %s; \"I\", \"me\" and \"my\" mean them.\n", p.Asker)
	} else {
		b.WriteString("The person asking is not a player.\n")
	}
	if len(p.Players) > 0 {
		fmt.Fprintf(&b, "Players: %s.\n", strings.Join(p.Players, ", "))
	}
	if p.Context != "" {
		b.WriteString("\nThe question replies to this earlier post of yours:\n<<<\n" + p.Context + "\n>>>\n")
	}
	return b.String()
}

// persona is the bot's voice. The fixed replies in the catalogues are
// written in it too; a change here is a change there. The limits are the
// recaps': a result somebody posted is theirs to be teased about, an
// absence is not, and nothing outside the game is fair game at all. A joke
// with a number in it fails the check in grounded, so it is told not to.
const persona = "Your personality: witty, dry and a little cocky, like a friend in the group " +
	"who keeps score and enjoys it too much. Tease a result someone posted, a failure included, " +
	"and brag on behalf of whoever leads. Never tease anyone for not playing, and never " +
	"about anything outside the game. The facts come first; the attitude is one short aside. " +
	"Your jokes contain no numbers.\n"

// offTopic reports whether an answer made without a lookup may be posted:
// it says something, and nothing in it is about the group. Without a
// lookup the model knows nothing about the group, so a player's name or a
// number in its answer is made up — "Bo leads", "an average of 3.4" — and
// a number from general knowledge cannot be told apart from one of those.
// A name is matched word by word, any part of it, so "Larsson" is Cid
// Larsson; a name that is also a word ("Bo") blocks that word too, which
// errs on the side of the unknown line.
func offTopic(answer string, players []store.Player) bool {
	if answer == "" || number.MatchString(answer) {
		return false
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(answer), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words[w] = true
	}
	for _, p := range players {
		for _, part := range strings.Fields(strings.ToLower(p.Name)) {
			if words[part] {
				return false
			}
		}
	}
	return true
}

// number is a figure as written: digits, with a decimal part after either
// separator, since the catalogues write "3,45" where the model may write
// "3.45".
var number = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// grounded reports whether every number in the answer appears in one of
// the sources. A decimal matches in either notation; a whole number must
// be one of the sources' whole numbers, not a part of a longer one.
func grounded(answer string, sources ...string) bool {
	known := map[string]bool{}
	for _, s := range sources {
		for _, n := range number.FindAllString(s, -1) {
			known[strings.ReplaceAll(n, ",", ".")] = true
			// "3,45" also holds 3 and 45 as they would read alone, which
			// is how a grouped thousand ("1 234") reads too.
			for _, part := range strings.FieldsFunc(n, func(r rune) bool { return r == ',' || r == '.' }) {
				known[part] = true
			}
		}
	}
	for _, n := range number.FindAllString(answer, -1) {
		if !known[strings.ReplaceAll(n, ",", ".")] {
			return false
		}
	}
	return true
}

// askAgent answers a question the Interpreter could not place. It posts
// the model's answer when it is grounded, the lookups when it is not, and
// the unknown line when the model looked nothing up — which also keeps the
// question, as an unplaced one always is.
func askAgent(ctx context.Context, t i18n.Translator, agent *Agent, p Prompt, asker *store.Player,
	players []store.Player, results []store.BoardResult,
	send func(context.Context, string) error, db *sql.DB, logger *slog.Logger) error {

	now := time.Now()
	look := func(name string, args json.RawMessage) (string, error) {
		return lookup(t, name, args, asker, players, results, now)
	}
	// The model stops short of the answer's deadline, so that what it
	// looked up can still be posted when it runs out of time.
	actx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		actx, cancel = context.WithDeadline(ctx, deadline.Add(-agentSendReserve))
		defer cancel()
	}
	text, looked, err := agent.Run(actx, p, look)
	switch {
	case errors.Is(err, errNoLookup) && offTopic(text, players):
		// Still kept: it is a question none of the kinds took.
		keepUnanswered(ctx, db, logger, p.Question)
		logger.Info("answering a question in the group", "kind", "agent", "lookups", 0)
		return send(ctx, text)
	case errors.Is(err, errNoLookup), errors.Is(err, ErrNotReady):
		keepUnanswered(ctx, db, logger, p.Question)
		return send(ctx, t.T("reply.unknown"))
	case err != nil && len(looked) == 0:
		_ = send(ctx, t.T("reply.failed"))
		keepUnanswered(ctx, db, logger, p.Question)
		return fmt.Errorf("agent: %w", err)
	}
	isGrounded := err == nil && text != "" && grounded(text, append(looked, p.Question, agentPrompt(p))...)
	logger.Info("answering a question in the group", "kind", "agent",
		"lookups", len(looked), "grounded", isGrounded)
	if !isGrounded {
		// Out of time, out of rounds, or a number from nowhere: what was
		// looked up is still a true answer to what was asked.
		text = strings.Join(slices.Compact(looked), "\n\n")
	}
	return send(ctx, text)
}
