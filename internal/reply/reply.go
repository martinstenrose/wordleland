// Package reply answers a question asked of the bot in the Signal group.
//
// A question arrives in whatever words and language the group uses. A
// language model turns it into a Request — which of a handful of things is
// being asked, over which span, about whom — and that is all the model does.
// The figures come from internal/stats, the same code the board runs, and
// the sentence from the i18n catalogues. A model that is wrong about a
// question produces the wrong answer to it; it can never produce a wrong
// number.
//
// Like internal/announce it sits above store, stats and i18n and below the
// bridge, which hands it a sender and a question and gets back an error or
// nothing. cmd/wordleland/serve.go does the wiring.
package reply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Kind is what a question asks for. The set is small on purpose: each one
// is a figure the board already computes, and a question outside it is
// answered with the list.
type Kind string

const (
	// KindLeader is who is best over a span: the leader and the ranking.
	KindLeader Kind = "leader"
	// KindStanding is where one player sits over a span.
	KindStanding Kind = "standing"
	// KindStreak is the longest run of solved days, going or ever.
	KindStreak Kind = "streak"
	// KindToday is today's puzzle: who has posted, the best so far, who is
	// missing.
	KindToday Kind = "today"
	// KindScore is one player's result on one day: "my score for July 5".
	KindScore Kind = "score"
	// KindWins is who has won the most months.
	KindWins Kind = "wins"
	// KindCatchup is whether somebody can still win the month: the gap to
	// the leader, the days left, and what it would take.
	KindCatchup Kind = "catchup"
	// KindCount is how many times a player has scored a given number, or
	// failed, or their whole distribution.
	KindCount Kind = "count"
	// KindHabits is who usually opens or closes the day, and when somebody
	// usually posts.
	KindHabits Kind = "habits"
	// KindRules is what a word in a post means: a miss, points, a streak.
	// Answered from the catalogues, never by the model, because how a
	// score is counted is the one thing the bot must not get creative
	// about.
	KindRules Kind = "rules"
	// KindThanks is praise or thanks with no question in it — "duktig
	// bot" — answered in kind rather than with the help line, which would
	// read as the bot missing the point.
	KindThanks Kind = "thanks"
	// KindHelp asks what the bot can do — "vad kan du?" — and gets the
	// list. Its own kind so that asking for help is not counted among the
	// questions the bot could not place.
	KindHelp Kind = "help"
	// KindWhatIf is a result that has not happened yet — "if Martin gets
	// a 6 tomorrow and Ibrahim a 3, who leads?" — scored into the month
	// with everyone else's results as they stand.
	KindWhatIf Kind = "whatif"
	// KindVersus is two players head to head: their averages over a span,
	// and the days both played, won, drawn and lost.
	KindVersus Kind = "versus"
	// KindDay is one day for everyone: each result, the group's average,
	// and how hard the puzzle was against the group's usual.
	KindDay Kind = "day"
	// KindPuzzles is the hardest puzzle over a span, or with Worst the
	// easiest: the group's average on it.
	KindPuzzles Kind = "puzzles"
	// KindDayWins is who has had the day's best score most often, ties
	// shared: a daily competition inside the month's.
	KindDayWins Kind = "daywins"
	// KindForm is who is playing best right now, over the board's form
	// window, and who has improved most against their own average; with
	// Worst, the other end.
	KindForm Kind = "form"
	// KindSteady is who is most consistent — the smallest spread of
	// scores — or with Worst the least predictable.
	KindSteady Kind = "steady"
	// KindWeekday is which day of the week is hardest for the group, or
	// a player's best and worst day of the week.
	KindWeekday Kind = "weekday"
	// KindProfile is everything about one player in one answer: "tell me
	// about Bo", "roast Alma".
	KindProfile Kind = "profile"
	// KindHistory is one player's months: how each went, and their best.
	KindHistory Kind = "history"
	// KindRecords is the group's all-time records.
	KindRecords Kind = "records"
	// KindGroup is the group as a whole: how many play, how many results,
	// the group's average.
	KindGroup Kind = "group"
	// KindUnknown is anything else: answered with one short line inviting
	// a question the bot can take, and kept for the owner to read, since
	// what the group asks and the bot cannot place is the next kind.
	KindUnknown Kind = "unknown"
)

// Kinds is every Kind, in the order the prompt describes them: the
// schema's enum and the parse's check both read it, so a kind cannot be
// added to one and not the other.
var Kinds = []Kind{KindLeader, KindStanding, KindStreak, KindToday, KindScore, KindWins,
	KindCatchup, KindCount, KindHabits, KindWhatIf, KindVersus, KindDay, KindPuzzles,
	KindDayWins, KindForm, KindSteady, KindWeekday, KindProfile, KindHistory, KindRecords,
	KindGroup, KindRules, KindThanks, KindHelp, KindUnknown}

// Topic is which rule a KindRules question asks about. Each has one
// catalogue text, written by hand to match what internal/stats does.
type Topic string

const (
	TopicMiss     Topic = "miss"
	TopicAverage  Topic = "average"
	TopicStreak   Topic = "streak"
	TopicMonth    Topic = "month"
	TopicHardMode Topic = "hardmode"
	TopicForm     Topic = "form"
	TopicRanked   Topic = "ranked"
	// TopicData is what the bot knows at all: the scores, never the words.
	TopicData Topic = "data"
)

// Topics is every Topic, in the order the "which one?" answer lists them.
var Topics = []Topic{TopicMiss, TopicAverage, TopicStreak, TopicMonth, TopicHardMode, TopicForm,
	TopicRanked, TopicData}

// Span is the window a leader or standing question covers.
type Span string

const (
	// SpanMonth is the current calendar month, and the default when a
	// question names no period: the month is the competition.
	SpanMonth Span = "month"
	// SpanDays is the last Request.Days puzzles, today's included.
	SpanDays Span = "days"
	// SpanAll is the whole history, which is the board's own ranking.
	SpanAll Span = "all"
	// SpanWeek is the calendar week so far, Monday to today: the week the
	// Sunday recap will close.
	SpanWeek Span = "week"
	// SpanLastWeek is the calendar week before it, Monday to Sunday.
	SpanLastWeek Span = "lastweek"
)

// Spans is every Span, for the schema's enum.
var Spans = []Span{SpanMonth, SpanDays, SpanAll, SpanWeek, SpanLastWeek}

// Request is a question reduced to what the answer needs. It is what the
// model produces, and the only thing it produces.
type Request struct {
	Kind Kind `json:"kind"`
	Span Span `json:"span"`
	// Days is how many, when Span is SpanDays.
	Days int `json:"days"`
	// Worst flips a leader question to the bottom of the table: who is
	// last, lowest, struggling.
	Worst bool `json:"worst"`
	// Player is the name the question is about, when it is about one
	// player, as it appears in the list the model was given. Empty when
	// the question names nobody.
	Player string `json:"player"`
	// Topic is the rule asked about, when Kind is KindRules.
	Topic Topic `json:"topic"`
	// Date is the day asked about, when Kind is KindScore, as YYYY-MM-DD.
	// The model resolves "yesterday" and "July 5" against the date it is
	// given; empty means today.
	Date string `json:"date"`
	// Month is a particular month a leader or standing question names, as
	// YYYY-MM: "vem vann juli?", "how did I do last month?". Empty means
	// the span the question says, the current month by default.
	Month string `json:"month"`
	// Guesses is the score a count question asks about: 1 to 6, 7 for a
	// failure, 0 for the whole distribution.
	Guesses int `json:"guesses"`
	// OrBetter widens a count question's Guesses to that score or better:
	// "3 or better", the board's own column.
	OrBetter bool `json:"orbetter"`
	// Other is the second player of a versus question.
	Other string `json:"other"`
	// Puzzle is a puzzle named by its number — "Wordle 1 900" — for a
	// score or day question; worked out to a date here, not by the model.
	Puzzle int `json:"puzzle"`
	// Scores are a what-if question's results that have not happened.
	Scores []Hypothetical `json:"scores"`
	// Also are the further questions of a message that asks more than
	// one — "who leads, and is my streak still going?" — each answered in
	// turn in the same post.
	Also []Request `json:"also"`
}

// Hypothetical is one made-up result: a player and their guesses, 1 to 6,
// or 7 for a failure.
type Hypothetical struct {
	Player  string `json:"player"`
	Guesses int    `json:"guesses"`
}

// Layouts for Request.Date and Request.Month.
const (
	DateLayout  = "2006-01-02"
	MonthLayout = "2006-01"
)

// Prompt is what the model is told besides the question: who is asking,
// who plays, and what day it is, so "me", a first name and "this week"
// resolve to something.
type Prompt struct {
	Question string
	// Asker is the questioner's player name, empty when the sender is not
	// a claimed identity.
	Asker   string
	Players []string
	Today   time.Time

	// Context is the bot's own earlier post the question replies to, when
	// it is a reply to one, so "what does this mean?" has a this. Only ever
	// the bot's own words — text this app wrote from its catalogues and
	// posted to the group — never another member's message.
	Context string
	// ContextDate is the day the quoted post is about, as YYYY-MM-DD, when
	// the post names a puzzle. Worked out here, not by the model: a puzzle
	// number is a date by arithmetic, and the model would only guess.
	ContextDate string
}

// keepUnanswered records a question the bot could not place, so a kind
// can be added for it later. Failing to record one is a warning, never a
// failed answer: the answer already went out.
func keepUnanswered(ctx context.Context, db *sql.DB, logger *slog.Logger, question string) {
	if err := store.RecordUnansweredQuestion(ctx, db, question); err != nil {
		logger.Warn("could not keep an unanswered question", "error", err)
	}
}

// puzzleInPost finds the puzzle a bot post is about: every recap opens
// with "Wordle <number>", written by i18n.Identifier without grouping.
var puzzleInPost = regexp.MustCompile(`Wordle (\d{3,5})\b`)

// mentionPlaceholder is the bridge's: the one name for the character Signal
// puts where a mention sits. Importing it is the one thing this package
// takes from the bridge, which does not import this one.
const mentionPlaceholder = bridge.MentionPlaceholder

// Interpreter turns a question into a Request. The one implementation
// talks to a language model; tests use a canned one.
type Interpreter interface {
	Interpret(ctx context.Context, p Prompt) (Request, error)
}

// ErrNotReady reports that the model is still being fetched or loaded.
// It is not a failure: the answer is "ask again in a bit", not a log line.
var ErrNotReady = errors.New("the language model is not ready yet")

// New returns the closure the bridge calls with a sender, their question
// with the mention already stripped from it, and the bot's own post the
// question replies to, or "" when it replies to nothing of the bot's.
//
// It reports nil when an answer was posted — including "I can't help with
// that" and "not ready yet", both of which are answers — and an error only
// when the store, the model or the send failed. The closure never logs the
// question: what was asked is the group's conversation, and the log carries
// only the request it became. A question the model could not place is the
// one exception, kept — text only, no sender — for thirty days, because it
// is the list of what to teach the bot next; see store.RecordUnansweredQuestion.
func New(db *sql.DB, cats i18n.Catalogues, locale string, interp Interpreter,
	send func(ctx context.Context, text string) error, logger *slog.Logger) func(context.Context, string, string, string, []string) error {

	// Rotating, so a line written more than one way is said in its next
	// wording each time the group hears it.
	t := i18n.NewTranslator(cats, locale).Rotating()

	return func(ctx context.Context, senderUUID, question, quoted string, mentioned []string) error {
		// Each mention is a placeholder in the text standing for an
		// account. The bot's own becomes nothing — it is the address, not
		// the question — and anybody else's becomes their player name, so
		// "how is @Bo doing?" reaches the model as "how is Bo doing?". An
		// account that is nobody's player, or a placeholder the mentions
		// did not account for, becomes nothing too.
		for _, uuid := range mentioned {
			name := ""
			if uuid != "" {
				if p, _, err := store.ResolveIdentity(ctx, db, "signal", uuid); err == nil {
					name = p.Name
				}
			}
			question = strings.Replace(question, mentionPlaceholder, name, 1)
		}
		question = strings.TrimSpace(strings.ReplaceAll(question, mentionPlaceholder, ""))
		if question == "" {
			// A bare mention. Saying what can be asked is the answer.
			return send(ctx, t.T("reply.help"))
		}

		// The asker is known when their Signal identity has been claimed
		// for a player, which is what lets "how am I doing" mean anything.
		// Unclaimed is ordinary — a member who has never posted a result —
		// and the question is answered without a "me".
		var asker *store.Player
		switch p, _, err := store.ResolveIdentity(ctx, db, "signal", senderUUID); {
		case err == nil:
			asker = &p
		case !errors.Is(err, store.ErrIdentityNotFound):
			return fmt.Errorf("resolve asker: %w", err)
		}

		req, text, err := placeAndAnswer(ctx, db, t, interp, asker, question, quoted)
		switch {
		case errors.Is(err, ErrNotReady):
			return send(ctx, t.T("reply.notready"))
		case errors.Is(err, errNotPlaced):
			// Best effort, and the model's failure is what is reported
			// whether or not the apology lands: it is the thing to fix.
			// Kept with the unplaced questions: whatever it was, the bot
			// did not answer it.
			_ = send(ctx, t.Vary("reply.failed"))
			keepUnanswered(ctx, db, logger, question)
			return err
		case err != nil:
			return err
		}
		also := make([]string, 0, len(req.Also))
		for _, a := range req.Also {
			also = append(also, string(a.Kind))
		}
		logger.Info("answering a question in the group",
			"kind", req.Kind, "span", req.Span, "days", req.Days, "named_player", req.Player != "",
			"also", strings.Join(also, ","))
		if req.Kind == KindUnknown {
			keepUnanswered(ctx, db, logger, question)
		}
		return send(ctx, text)
	}
}

// errNotPlaced is the model failing to place a question at all, as distinct
// from placing it as unknown.
var errNotPlaced = errors.New("interpret question")

// placeAndAnswer is the bot's work on one question, short of posting it:
// the model places it, and the history answers it. It reads and writes
// nothing else, so the bot and `wordleland ask` give the same answer.
func placeAndAnswer(ctx context.Context, db *sql.DB, t i18n.Translator, interp Interpreter,
	asker *store.Player, question, quoted string) (Request, string, error) {

	players, err := store.ListPlayers(ctx, db)
	if err != nil {
		return Request{}, "", fmt.Errorf("list players: %w", err)
	}
	results, err := store.ResultsForBoard(ctx, db)
	if err != nil {
		return Request{}, "", fmt.Errorf("read results: %w", err)
	}
	names := make([]string, 0, len(players))
	for _, p := range players {
		names = append(names, p.Name)
	}
	prompt := Prompt{Question: question, Players: names, Today: time.Now()}
	if asker != nil {
		prompt.Asker = asker.Name
	}
	if quoted = strings.TrimSpace(quoted); quoted != "" {
		prompt.Context = quoted
		if m := puzzleInPost.FindStringSubmatch(quoted); m != nil {
			if puzzle, err := strconv.Atoi(m[1]); err == nil {
				if date, err := wordle.DateForPuzzle(puzzle); err == nil {
					prompt.ContextDate = date.Format(DateLayout)
				}
			}
		}
	}

	req, err := interp.Interpret(ctx, prompt)
	switch {
	case errors.Is(err, ErrNotReady):
		return Request{}, "", err
	case err != nil:
		return Request{}, "", fmt.Errorf("%w: %w", errNotPlaced, err)
	}
	req = withAsker(req, asker)
	now := time.Now()
	answers := []string{answer(t, req, asker, players, results, now)}
	for _, a := range req.Also {
		answers = append(answers, answer(t, a, asker, players, results, now))
	}
	return req, strings.Join(answers, "\n\n"), nil
}

// withAsker fills the asker's name in where the model wrote a pronoun for
// them, in the request and everything it carries. With nobody asking, the
// pronoun stays, and the answer asks who is meant.
func withAsker(req Request, asker *store.Player) Request {
	if asker == nil {
		return req
	}
	name := asker.Name
	if req.Player == Asker {
		req.Player = name
	}
	if req.Other == Asker {
		req.Other = name
	}
	if len(req.Scores) > 0 {
		scores := slices.Clone(req.Scores)
		for i := range scores {
			if scores[i].Player == Asker {
				scores[i].Player = name
			}
		}
		req.Scores = scores
	}
	if len(req.Also) > 0 {
		also := make([]Request, len(req.Also))
		for i, a := range req.Also {
			also[i] = withAsker(a, asker)
		}
		req.Also = also
	}
	return req
}

// Ask places and answers one question as the bot would for the player
// named — nobody, when the name is empty — and returns the request and
// the answer rather than posting anything. Nothing is written either: a
// question it could not place is not kept, since whoever runs this is
// trying the bot out, not asking the group.
func Ask(ctx context.Context, db *sql.DB, cats i18n.Catalogues, locale string, interp Interpreter,
	player, question string) (Request, string, error) {

	t := i18n.NewTranslator(cats, locale)
	var asker *store.Player
	if player != "" {
		players, err := store.ListPlayers(ctx, db)
		if err != nil {
			return Request{}, "", fmt.Errorf("list players: %w", err)
		}
		p, ok := findPlayer(player, players)
		if !ok {
			return Request{}, "", fmt.Errorf("no player %q", player)
		}
		asker = &p
	}
	return placeAndAnswer(ctx, db, t, interp, asker, strings.TrimSpace(question), "")
}
