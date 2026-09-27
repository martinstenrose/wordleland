package reply

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
// Prepare does, pulls the model if it is missing, and refuses one the
// server says cannot call tools — the agent is then never ready, and its
// questions get the unknown line as they would without it.
func NewAgent(url, model string) *Agent {
	o := NewOllama(url, model)
	o.needs = []string{"tools"}
	return &Agent{Ollama: o}
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

// errLookupsFailed is a model that tried to look something up, failed
// every time, and answered anyway. It was a question about the group — it
// reached for the tools — so what it said is unchecked figures about the
// group, and is never posted.
var errLookupsFailed = errors.New("every lookup the model tried failed")

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
	{toolProfile, "Everything about one player at once: their standing all time and this month, streaks, score distribution, months won and posting habits. For \"tell me about\", \"roast\" or comparing players.",
		[]string{"player"}},
	{toolResults, "A player's result on each day from one date to another, at most 62 days: the number of guesses, X for a failure, - for a day not played, * for hard mode.",
		[]string{"player", "from", "to"}},
}

// The tools that are not a Kind.
const (
	toolResults = "results"
	toolProfile = "profile"
)

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

	text, err := lookupText(t, name, args, asker, players, results, now)
	if err != nil {
		return "", err
	}
	// A lookup that found no player answers with who it could have been:
	// the whole roster, as found. That is not a finding — it would count
	// as a lookup that worked and put every name among those the model
	// has seen — so it goes back to the model as an error instead.
	var fields map[string]any
	_ = json.Unmarshal(args, &fields)
	player, _ := fields["player"].(string)
	player = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(player), "@"))
	if _, ok, miss := whom(t, Request{Player: player}, asker, players); !ok && text == miss {
		return "", errors.New(miss)
	}
	return text, nil
}

func lookupText(t i18n.Translator, name string, args json.RawMessage, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) (string, error) {

	var fields map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return "", fmt.Errorf("arguments are not an object: %w", err)
		}
	}
	if fields == nil {
		// No arguments, or "arguments": null, which a model sends for a
		// tool that takes none.
		fields = map[string]any{}
	}
	if !slices.ContainsFunc(tools, func(tl tool) bool { return tl.name == name }) {
		return "", fmt.Errorf("no such tool %q", name)
	}
	lenient(fields)
	switch name {
	case toolResults:
		return resultsList(t, fields, asker, players, results, now)
	case toolProfile:
		return profile(t, fields, asker, players, results, now), nil
	}
	fields["kind"] = name
	raw, _ := json.Marshal(fields)
	req, err := parseRequest(string(raw))
	if err != nil {
		return "", err
	}
	return answer(t, req, asker, players, results, now), nil
}

// profile is one player's answers to every kind about a player, in one
// lookup: a small model asked to "roast Bo" or compare two players does
// better with one call per player than with six it has to think of.
func profile(t i18n.Translator, fields map[string]any, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	player, _ := fields["player"].(string)
	p, ok, text := whom(t, Request{Player: player}, asker, players)
	if !ok {
		return text
	}
	var lines []string
	for _, req := range []Request{
		{Kind: KindStanding, Span: SpanAll},
		{Kind: KindStanding, Span: SpanMonth},
		{Kind: KindStreak},
		{Kind: KindCount},
		{Kind: KindWins},
		{Kind: KindHabits},
	} {
		req.Player = p.Name
		lines = append(lines, answer(t, req, asker, players, results, now))
	}
	return strings.Join(lines, "\n")
}

// lenient repairs what small models get wrong about arguments in ways that
// have one reading: a number or a yes/no written as a string ("7",
// "true"), and a name written as a mention ("@Bo"). Anything else is left
// for parseRequest to refuse, which tells the model.
func lenient(fields map[string]any) {
	for _, k := range []string{"days", "guesses"} {
		if s, ok := fields[k].(string); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				fields[k] = n
			}
		}
	}
	if s, ok := fields["worst"].(string); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			fields["worst"] = b
		}
	}
	if s, ok := fields["player"].(string); ok {
		fields["player"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	}
}

// resultsList is a player's results day by day, for questions the answers
// do not cover: "what did Bo get this week?", "did I play on Sunday?".
func resultsList(t i18n.Translator, fields map[string]any, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) (string, error) {

	player, _ := fields["player"].(string)
	p, ok, text := whom(t, Request{Player: player}, asker, players)
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
		// The group's own words for the day, as the answers write dates,
		// since this list is also what the group reads when the model's
		// sentence fails its check.
		label := t.T("reply.date", date.Day(), t.T("month."+strconv.Itoa(int(date.Month()))))
		lines = append(lines, t.T("weekday."+strconv.Itoa(int(date.Weekday())))+" "+label+": "+mark)
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

// lookedUp is one lookup the model made, and what it returned.
type lookedUp struct {
	tool string
	text string
}

// Run lets the model look things up until it answers, and reports the
// answer with every lookup's text, which is what the answer is checked
// against and what replaces it when the check fails. errNoLookup, with
// what the model said, when it answered without looking anything up.
func (a *Agent) Run(ctx context.Context, p Prompt,
	look func(name string, args json.RawMessage) (string, error)) (runResult, error) {

	if !a.Ready() {
		return runResult{}, ErrNotReady
	}
	messages := []chatMessage{{Role: "system", Content: agentPrompt(p)}}
	for _, h := range p.History {
		messages = append(messages,
			chatMessage{Role: "user", Content: saidBy(h.Asker, h.Question)},
			chatMessage{Role: "assistant", Content: h.Answer})
	}
	messages = append(messages, chatMessage{Role: "user", Content: saidBy(p.Asker, p.Question)})
	var looked []lookedUp
	tried := false
	for range maxAgentRounds {
		reply, err := a.chat(ctx, messages)
		if err != nil {
			return runResult{looked: looked}, err
		}
		if len(reply.ToolCalls) == 0 {
			if len(looked) == 0 && tried {
				return runResult{}, errLookupsFailed
			}
			out := runResult{text: strings.TrimSpace(reply.Content), looked: looked, transcript: messages}
			if len(looked) == 0 {
				return out, errNoLookup
			}
			return out, nil
		}
		calls := reply.ToolCalls
		if len(calls) > maxCallsInRound {
			calls = calls[:maxCallsInRound]
		}
		messages = append(messages, chatMessage{Role: "assistant", Content: reply.Content, ToolCalls: calls})
		for _, c := range calls {
			// A tool that does not exist — "web_search" for the capital
			// of Sweden — is a model reaching past the game, not into it.
			tried = tried || slices.ContainsFunc(tools, func(tl tool) bool { return tl.name == c.Function.Name })
		}
		for _, c := range calls {
			text, err := look(c.Function.Name, callArguments(c.Function.Arguments))
			if err != nil {
				// Told to the model, which can call again properly.
				text = "Error: " + err.Error()
			} else {
				looked = append(looked, lookedUp{c.Function.Name, text})
			}
			messages = append(messages, chatMessage{Role: "tool", Content: text, ToolName: c.Function.Name})
		}
	}
	// Out of rounds with lookups in hand: those are the answer.
	if len(looked) == 0 {
		return runResult{}, errLookupsFailed
	}
	return runResult{looked: looked}, nil
}

// runResult is what Run ends with: the model's answer, what it looked
// up, and the conversation that led to the answer, which a repair
// continues.
type runResult struct {
	text       string
	looked     []lookedUp
	transcript []chatMessage
}

// repairMinLeft is the time a repair needs before the answer's deadline:
// one more round of the model. With less, the lookups are posted.
const repairMinLeft = 45 * time.Second

// repair gives the model one chance to rewrite an answer that failed the
// checks, told exactly what failed. It may not look anything else up: a
// repair is a rewrite of what it already has.
func (a *Agent) repair(ctx context.Context, transcript []chatMessage, draft, problem string) (string, error) {
	messages := append(slices.Clone(transcript),
		chatMessage{Role: "assistant", Content: draft},
		chatMessage{Role: "user", Content: problem})
	reply, err := a.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	if len(reply.ToolCalls) > 0 {
		return "", errors.New("the model looked something up instead of rewriting")
	}
	return strings.TrimSpace(reply.Content), nil
}

// repairRequest tells the model what in its answer no lookup backed.
// English, as all its instructions are; the figures and names are its own.
func repairRequest(numbers, names []string) string {
	var what []string
	if len(numbers) > 0 {
		what = append(what, "the numbers "+strings.Join(numbers, ", "))
	}
	if len(names) > 0 {
		what = append(what, "the players "+strings.Join(names, ", "))
	}
	return "Your answer mentions " + strings.Join(what, " and ") + ", which nothing you looked up " +
		"says. Rewrite it using only what the lookups said, in the same voice and as short, " +
		"in the language of the question. Answer with the rewritten message only."
}

// saidBy is a question as the agent is shown it: prefixed with who asked,
// since the conversation it is shown has several people in it.
func saidBy(asker, question string) string {
	if asker == "" {
		return question
	}
	return asker + ": " + question
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
		"answered from the tools, never from memory. Anything else you may answer from what you know, " +
		"in one or two sentences, and leave the game out of that answer: the bot adds a line about " +
		"Wordle after it. In an answer without the tools, call the person asking \"you\" and name " +
		"no one. If you are not sure, say you don't know.\n")
	if len(p.History) > 0 {
		b.WriteString("The messages before the last are the recent conversation, each question " +
			"prefixed with who asked. Use them to read a follow-up (\"and last week?\", \"what about " +
			"Bo?\"), but answer only the last message, and look its figures up again.\n")
	}
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
// absence is not, and nothing about a person but their Wordle is fair
// game. A joke with a number in it fails the check in grounded, so it is
// told not to; a number written as a word escapes the check altogether, so
// it is told to write digits. The examples carry no numbers either: the
// instructions vouch for the numbers in them, so every number here is one
// the model could repeat unchecked.
const persona = "Your personality: witty, dry and a little cocky, like a friend in the group " +
	"who keeps score and enjoys it too much. Tease a result someone posted, a failure included, " +
	"and brag on behalf of whoever leads. Never tease anyone for not playing, and never tease " +
	"anyone about anything but their Wordle. The facts come first; the attitude is one short " +
	"aside. Your jokes contain no numbers, and you write every number as digits.\n" +
	"The voice, by example (the name is made up):\n" +
	"- Sam leads the month, and has started walking differently.\n" +
	"- An X yesterday. We light a candle and move on.\n" +
	"- The capital of Sweden is Stockholm. Next you'll ask me what colour the sky is.\n"

// offTopic reports whether an answer made without a lookup may be posted:
// it says something, and names no player. Without a lookup the model knows
// nothing about the group, so a player in its answer is made up — "Bo
// leads". Numbers are let through: a year or a distance in a general
// answer is the point of answering, and an invented figure about the group
// with nobody named in it is the risk taken for that. A name is matched
// word by word, any part of it, so "Larsson" is Cid Larsson; a name that
// is also a word ("Bo") blocks that word too, which errs on the side of the
// unknown line.
//
// The asker is no exception, though "Hej Bo!" to Bo is harmless: "Bo
// leads" to Bo is not, and the two cannot be told apart. The model is
// told to say "you" instead, which is the same greeting.
func offTopic(answer string, players []store.Player) bool {
	return answer != "" && len(namedPlayers(answer, players)) == 0
}

// namedPlayers is the players a text mentions, by any part of their name
// as a whole word, ignoring case — "Anna-Karin" by "Anna" or "Karin" —
// and in the possessive, which Swedish writes with a bare s: "Bos snitt",
// "Karins svit".
func namedPlayers(text string, players []store.Player) []int64 {
	words := map[string]bool{}
	for _, w := range nameWords(text) {
		words[w] = true
	}
	var out []int64
	for _, p := range players {
		for _, part := range nameWords(p.Name) {
			if words[part] || words[part+"s"] {
				out = append(out, p.ID)
				break
			}
		}
	}
	return out
}

// nameWords splits a text the same way for names and for answers: into
// runs of letters and digits, lower case, so a hyphen or an apostrophe in
// either cannot make a name unmatchable.
func nameWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// namesGrounded reports whether every player the answer names is named in
// the sources too. It does not catch a score pinned on the wrong one of
// two players the lookups both mention; it does catch a player brought in
// from nowhere, which is how a model filling in a sentence goes wrong.
func namesGrounded(answer string, players []store.Player, sources ...string) bool {
	return len(ungroundedNames(answer, players, sources...)) == 0
}

// ungroundedNames is the players the answer names that no source does.
func ungroundedNames(answer string, players []store.Player, sources ...string) []string {
	known := namedPlayers(strings.Join(sources, "\n"), players)
	var out []string
	for _, id := range namedPlayers(answer, players) {
		if !slices.Contains(known, id) {
			i := slices.IndexFunc(players, func(p store.Player) bool { return p.ID == id })
			out = append(out, players[i].Name)
		}
	}
	return out
}

// maxAnswerRunes is the longest answer posted as the model wrote it: about
// five short sentences. The model is asked for one to three; past this it
// is rambling, and a chat is not the place.
const maxAnswerRunes = 500

var sentenceEnds = regexp.MustCompile(`[.!?…]\s|\n`)

// tidy makes the model's text fit a chat: Signal shows markdown as the
// characters, so the markers go, and a long answer is cut after the last
// whole sentence that fits, or at a word with an ellipsis when none does.
func tidy(text string) string {
	text = strings.NewReplacer("**", "", "__", "", "`", "").Replace(text)
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		lines = append(lines, strings.TrimLeft(l, "# "))
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	r := []rune(text)
	if len(r) <= maxAnswerRunes {
		return text
	}
	cut := string(r[:maxAnswerRunes])
	// A sentence ends at a mark followed by a space or a line break, or at
	// the cut itself: the point in "3.45" ends nothing.
	if loc := sentenceEnds.FindAllStringIndex(cut+" ", -1); len(loc) > 0 {
		return strings.TrimSpace(cut[:loc[len(loc)-1][0]+1])
	}
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// number is a figure as written: digits, with a decimal part after either
// separator, since the catalogues write "3,45" where the model may write
// "3.45".
//
// Any script's digits, not only ASCII: a number written in other digits
// is still a number, and it matches nothing the lookups wrote.
var number = regexp.MustCompile(`\p{Nd}+(?:[.,]\p{Nd}+)?`)

// grounded reports whether every number in the answer appears in one of
// the sources. A decimal matches in either notation, and its whole part
// counts too, since "4.00 on average" is fairly said as "4" — but not its
// fraction, or "3,45" would vouch for a 45. Leading zeros do not count:
// "2026-09-01" holds the 1 of "1 September".
func grounded(answer string, sources ...string) bool {
	return len(ungroundedNumbers(answer, sources...)) == 0
}

// ungroundedNumbers is the numbers in the answer no source vouches for,
// each once, as the answer wrote them.
func ungroundedNumbers(answer string, sources ...string) []string {
	known := map[string]bool{}
	for _, s := range sources {
		for _, n := range number.FindAllString(s, -1) {
			known[canonicalNumber(n)] = true
			whole, _, _ := strings.Cut(strings.ReplaceAll(n, ",", "."), ".")
			known[canonicalNumber(whole)] = true
		}
	}
	var out []string
	for _, n := range number.FindAllString(answer, -1) {
		if !known[canonicalNumber(n)] && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// canonicalNumber is a number as grounded compares it: a point for the
// decimal separator, no leading zeros on the whole part and no trailing
// ones on the fraction.
func canonicalNumber(n string) string {
	n = strings.ReplaceAll(n, ",", ".")
	whole, frac, isDecimal := strings.Cut(n, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	// "3,40" and "3.4" are the same number, and so are "4.00" and "4".
	if frac = strings.TrimRight(frac, "0"); isDecimal && frac != "" {
		return whole + "." + frac
	}
	return whole
}

// fallback is the lookups posted in place of the model's sentence: each
// once, and without the day-by-day lists when there is anything else, since
// those are working material for the model and a wall of dates in a chat.
// When the lists are all there is, each is cut to its last fallbackDays.
func fallback(looked []lookedUp) string {
	var out []string
	lists := 0
	for _, l := range looked {
		if l.tool == toolResults {
			lists++
		}
	}
	for _, l := range looked {
		if l.tool == toolResults && lists < len(looked) {
			continue
		}
		text := l.text
		if l.tool == toolResults {
			text = lastDays(text, fallbackDays)
		}
		if !slices.Contains(out, text) {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n\n")
}

// asked is one question and what it is answered from.
type asked struct {
	t       i18n.Translator
	prompt  Prompt
	asker   *store.Player
	players []store.Player
	results []store.BoardResult
	now     time.Time
}

// timeLeft is how long until ctx's deadline, or forever without one.
func timeLeft(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return time.Until(d)
	}
	return time.Duration(math.MaxInt64)
}

// failedCheck names the checks an answer failed, for the log: which one
// fails, and how often, says whether the prompt or the model needs work.
// Never the answer or the figures themselves.
func failedCheck(nums, names []string) string {
	switch {
	case len(nums) > 0 && len(names) > 0:
		return "numbers+names"
	case len(nums) > 0:
		return "numbers"
	case len(names) > 0:
		return "names"
	}
	return ""
}

// fallbackDays is how much of a day-by-day list is posted as a fallback:
// the week the question was most likely about.
const fallbackDays = 7

// lastDays keeps a list's heading and its last n days.
func lastDays(list string, n int) string {
	lines := strings.Split(list, "\n")
	if len(lines) <= n+1 {
		return list
	}
	return strings.Join(append(lines[:1:1], lines[len(lines)-n:]...), "\n")
}

// askAgent answers a question the Interpreter could not place, and
// remembers the turn. It posts:
//
//   - the model's answer when it looked something up and every number in it
//     is from a lookup, and the lookups when not;
//   - an answer made without a lookup, with a line steering back to the
//     game, when it names no player and the conversation has not already
//     been off-topic maxOffTopicInARow times — past that, a line turning
//     the question away, with the same steer;
//   - the unknown line when the model named a player without looking
//     anything up.
//
// Every question it takes is kept with the unplaced ones, as each is a
// question none of the kinds took.
func askAgent(ctx context.Context, q asked, agent *Agent, conv *conversation,
	send func(context.Context, string) error, db *sql.DB, logger *slog.Logger) error {

	t, p := q.t, q.prompt
	keepUnanswered(ctx, db, logger, p.Question)
	remember := func(text string, tp topic) {
		conv.add(turn{at: q.now, asker: p.Asker, question: p.Question, answer: text, topic: tp})
	}
	look := func(name string, args json.RawMessage) (string, error) {
		return lookup(t, name, args, q.asker, q.players, q.results, q.now)
	}
	// The model stops short of the answer's deadline, so that what it
	// looked up can still be posted when it runs out of time.
	actx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		actx, cancel = context.WithDeadline(ctx, deadline.Add(-agentSendReserve))
		defer cancel()
	}
	res, err := agent.Run(actx, p, look)
	text, looked := tidy(res.text), res.looked
	switch {
	case errors.Is(err, errNoLookup) && offTopic(text, q.players):
		steer := segue(t, conv.nextSegue(), q.players, q.results, q.now)
		if run := conv.offTopicRun(q.now); run >= maxOffTopicInARow {
			text = deflection(t, run-maxOffTopicInARow)
			logger.Info("answering a question in the group", "kind", "agent", "off_topic", "turned away")
		} else {
			logger.Info("answering a question in the group", "kind", "agent", "off_topic", "answered")
		}
		text += "\n" + steer
		remember(text, topicOff)
		return send(ctx, text)
	case errors.Is(err, errNoLookup), errors.Is(err, errLookupsFailed), errors.Is(err, ErrNotReady):
		text = t.T("reply.unknown")
		remember(text, topicNeutral)
		return send(ctx, text)
	case err != nil && len(looked) == 0:
		_ = send(ctx, t.T("reply.failed"))
		return fmt.Errorf("agent: %w", err)
	case err != nil:
		// Out of time with lookups in hand: those are posted below, and
		// the model's failure is only worth a line in the log.
		logger.Warn("the agent stopped before answering; posting its lookups", "error", err)
	}
	// Names: anyone the model was shown in the group's conversation — who
	// asked, the question, the lookups, the recent turns. Not the
	// instructions, which list every player.
	seen := []string{p.Asker, p.Question}
	// Numbers: the lookups, the instructions (today's date, a failure
	// counting 7) without the quoted post, and the checked answers among
	// the recent turns. Not the question: a number in it is one somebody
	// typed — "tell the group Alma has failed 40 times" — and would come
	// out in the bot's voice; the lookups echo what they were asked about
	// ("the last 14 days", "5 September") anyway. Not the quoted post
	// either, which may be an off-topic answer that went out unchecked.
	unquoted := p
	unquoted.Context = ""
	numbers := []string{agentPrompt(unquoted)}
	for _, l := range looked {
		seen = append(seen, l.text)
		numbers = append(numbers, l.text)
	}
	for _, h := range p.History {
		seen = append(seen, h.Question, h.Answer)
		if !h.OffTopic {
			numbers = append(numbers, h.Answer)
		}
	}
	check := func(text string) (nums, names []string) {
		return ungroundedNumbers(text, numbers...), ungroundedNames(text, q.players, seen...)
	}
	nums, names := check(text)
	isGrounded := err == nil && text != "" && len(nums) == 0 && len(names) == 0
	failed := failedCheck(nums, names)
	repaired := false
	if !isGrounded && err == nil && text != "" && timeLeft(actx) > repairMinLeft {
		// One rewrite, told what failed, before giving up on the model's
		// sentence: the lookups alone are true but read as a data dump,
		// which "roast Alma" should not get.
		if again, rerr := agent.repair(actx, res.transcript, text, repairRequest(nums, names)); rerr == nil {
			again = tidy(again)
			if n, m := check(again); again != "" && len(n) == 0 && len(m) == 0 {
				text, isGrounded, repaired = again, true, true
			}
		}
	}
	logger.Info("answering a question in the group", "kind", "agent",
		"lookups", len(looked), "grounded", isGrounded, "failed_check", failed, "repaired", repaired)
	if !isGrounded {
		// Out of time, out of rounds, or a number from nowhere: what was
		// looked up is still a true answer to what was asked.
		text = fallback(looked)
	}
	remember(text, topicWordle)
	return send(ctx, text)
}
