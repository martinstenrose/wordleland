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
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
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
	// KindRules is what a word in a post means: a miss, points, a streak.
	// Answered from the catalogues, never by the model, because how a
	// score is counted is the one thing the bot must not get creative
	// about.
	KindRules Kind = "rules"
	// KindUnknown is anything else, answered with what can be asked.
	KindUnknown Kind = "unknown"
)

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
)

// Topics is every Topic, in the order the "which one?" answer lists them.
var Topics = []Topic{TopicMiss, TopicAverage, TopicStreak, TopicMonth, TopicHardMode, TopicForm, TopicRanked}

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
)

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
}

// DateLayout is how Request.Date is written.
const DateLayout = "2006-01-02"

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
}

// Interpreter turns a question into a Request. The one implementation
// talks to a language model; tests use a canned one.
type Interpreter interface {
	Interpret(ctx context.Context, p Prompt) (Request, error)
}

// ErrNotReady reports that the model is still being fetched or loaded.
// It is not a failure: the answer is "ask again in a bit", not a log line.
var ErrNotReady = errors.New("the language model is not ready yet")

// New returns the closure the bridge calls with a sender and their
// question, the mention already stripped from it.
//
// It reports nil when an answer was posted — including "I can't help with
// that" and "not ready yet", both of which are answers — and an error only
// when the store, the model or the send failed. The closure never logs the
// question: what was asked is the group's conversation, and the log carries
// only the request it became.
func New(db *sql.DB, cats i18n.Catalogues, locale string, interp Interpreter,
	send func(ctx context.Context, text string) error, logger *slog.Logger) func(context.Context, string, string) error {

	t := i18n.NewTranslator(cats, locale)

	return func(ctx context.Context, senderUUID, question string) error {
		question = strings.TrimSpace(question)
		if question == "" {
			// A bare mention. Saying what can be asked is the answer.
			return send(ctx, t.T("reply.help"))
		}

		players, err := store.ListPlayers(ctx, db)
		if err != nil {
			return fmt.Errorf("list players: %w", err)
		}
		results, err := store.ResultsForBoard(ctx, db)
		if err != nil {
			return fmt.Errorf("read results: %w", err)
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

		names := make([]string, 0, len(players))
		for _, p := range players {
			names = append(names, p.Name)
		}
		prompt := Prompt{Question: question, Players: names, Today: time.Now()}
		if asker != nil {
			prompt.Asker = asker.Name
		}

		req, err := interp.Interpret(ctx, prompt)
		switch {
		case errors.Is(err, ErrNotReady):
			return send(ctx, t.T("reply.notready"))
		case err != nil:
			// Best effort, and the model's failure is what is reported
			// whether or not the apology lands: it is the thing to fix.
			_ = send(ctx, t.T("reply.failed"))
			return fmt.Errorf("interpret question: %w", err)
		}
		logger.Info("answering a question in the group",
			"kind", req.Kind, "span", req.Span, "days", req.Days, "named_player", req.Player != "")

		return send(ctx, answer(t, req, asker, players, results, time.Now()))
	}
}
