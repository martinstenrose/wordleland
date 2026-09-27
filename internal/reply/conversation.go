package reply

import (
	"strconv"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// topic is what an answered question turned out to be about, which is what
// decides whether the next off-topic question gets an answer.
type topic int

const (
	// topicNeutral is neither: thanks, help, the unknown line. It does not
	// break a run of off-topic answers, and does not extend one.
	topicNeutral topic = iota
	// topicWordle is an answer about the game, which ends a run.
	topicWordle
	// topicOff is an off-topic question answered, or turned away: both
	// are the conversation drifting, and both count toward the run.
	topicOff
)

// turn is one question and the answer the group saw.
type turn struct {
	at       time.Time
	asker    string
	question string
	answer   string
	topic    topic
}

// conversation is the bot's short memory of what it was just asked, for
// two things: a follow-up — "and last week?" — that means nothing on its
// own, and noticing that the group has asked it about anything but Wordle
// several times in a row.
//
// It lives in memory and nowhere else, for memoryWindow: the questions are
// the group's conversation, which is why the log never carries them and
// why this is not written down either. Every read drops what has aged out,
// and a timer drops the rest a window after the last question, so the
// evening's last question does not sit in memory until the next one. A
// restart forgets it, which costs a follow-up at most.
type conversation struct {
	mu    sync.Mutex
	turns []turn
	// segues counts the lines steering back to the game, so consecutive
	// ones say different things.
	segues int
	// forget empties turns a window after the last one was added.
	forget *time.Timer
	// quiet is memoryWindow, shortened in a test that waits for forget.
	quiet time.Duration
	// active is the wall-clock time of the last question read or answer
	// kept. forget clears only once that is a window old, so a question
	// being answered as the window closes keeps its conversation.
	active time.Time
}

const (
	// memoryWindow is the quiet that ends a conversation. A follow-up
	// comes within minutes; a question after a quarter of an hour of
	// nothing is a new conversation, and the off-topic count starts again
	// with it. Measured from the last turn, not each: a conversation that
	// keeps going is one conversation, so spacing off-topic questions
	// fourteen minutes apart does not reset the count.
	memoryWindow = 15 * time.Minute
	// maxKept is how many turns are held at most: enough to count an
	// off-topic run and show maxTurns, and a bound on what is held while
	// a conversation goes on.
	maxKept = 4 * maxTurns
	// maxTurns is how many turns the agent is shown. More costs the model
	// time on every question for context that is rarely used.
	maxTurns = 4
	// maxOffTopicInARow is how many off-topic questions in a row are
	// answered before the next is turned away. Two, not one, because a
	// greeting is off-topic too, and "hej!" should not cost the question
	// after it its answer.
	maxOffTopicInARow = 2
)

// add remembers a turn, and forgets what has aged out.
func (c *conversation) add(t turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = append(c.recentLocked(t.at), t)
	if len(c.turns) > maxKept {
		c.turns = c.turns[len(c.turns)-maxKept:]
	}
	c.touchLocked()
}

// touchLocked marks the conversation active now and (re)arms forget.
func (c *conversation) touchLocked() {
	c.active = time.Now()
	if c.forget == nil {
		c.forget = time.AfterFunc(c.window(), c.clear)
	} else {
		c.forget.Reset(c.window())
	}
}

func (c *conversation) window() time.Duration {
	if c.quiet > 0 {
		return c.quiet
	}
	return memoryWindow
}

// clear forgets every turn once the conversation has been quiet for a
// window. A question read or an answer kept since the timer was set — the
// two can race it — means it is not quiet yet, and the timer is set again
// for what is left.
func (c *conversation) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if left := c.window() - time.Since(c.active); left > 0 {
		c.forget.Reset(left)
		return
	}
	c.turns = nil
}

// recent is the last maxTurns turns of the conversation still going at
// now, oldest first.
func (c *conversation) recent(now time.Time) []turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = c.recentLocked(now)
	if len(c.turns) > 0 {
		// A question is being answered: the conversation is live until its
		// answer is kept, however long that takes.
		c.touchLocked()
	}
	out := c.turns
	if len(out) > maxTurns {
		out = out[len(out)-maxTurns:]
	}
	return append([]turn(nil), out...)
}

// recentLocked is the conversation still going at now: every turn held,
// or none once the newest is a window old.
func (c *conversation) recentLocked(now time.Time) []turn {
	var newest time.Time
	for _, t := range c.turns {
		if t.at.After(newest) {
			newest = t.at
		}
	}
	if now.Sub(newest) > c.window() {
		return nil
	}
	return c.turns
}

// offTopicRun is how many off-topic turns the conversation still going at
// now has had since the last one about the game.
func (c *conversation) offTopicRun(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = c.recentLocked(now)
	turns := c.turns
	run := 0
	for i := len(turns) - 1; i >= 0; i-- {
		switch turns[i].topic {
		case topicWordle:
			return run
		case topicOff:
			run++
		}
	}
	return run
}

// nextSegue is a counter for choosing the next steering line.
func (c *conversation) nextSegue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.segues++
	return c.segues - 1
}

// Deflections are the lines for an off-topic question past the run, one
// per key, taken in turn.
const deflections = 3

// deflection is the n-th line turning an off-topic question away.
func deflection(t i18n.Translator, n int) string {
	return t.T("reply.offtopic." + strconv.Itoa(n%deflections))
}

// segue is a line steering the conversation back to the game, with a real
// figure in it where there is one: who leads the month, the longest
// running streak, how many have played today. They take turns, starting
// at n, and one with nothing to say gives way to the next; the plain line
// is left when none has. A tie gives way too: the lines are one person's,
// and a tie is on the board for whoever asks. Built from stats like every answer, and counting
// who has played today rather than naming who has not, as the recaps do.
func segue(t i18n.Translator, n int, players []store.Player, results []store.BoardResult, now time.Time) string {
	lines := []func() string{
		func() string {
			opts := stats.DefaultOptions(now)
			for _, m := range stats.ComputeMonths(players, results, opts) {
				if m.Year != now.Year() || m.Month != now.Month() || len(m.Winners) != 1 {
					continue
				}
				return t.T("reply.segue.leader", m.Winners[0].Name,
					t.T("month."+strconv.Itoa(int(now.Month()))), t.Decimal(*m.Winners[0].Average, 2))
			}
			return ""
		},
		func() string {
			board := stats.Compute(players, results, stats.DefaultOptions(now))
			all := append(append([]stats.Player(nil), board.Ranked...), board.Unranked...)
			who, days := holders(all, func(p stats.Player) int { return p.CurrentStreak })
			// A streak of a day or two is not news.
			if days < 3 || len(who) != 1 {
				return ""
			}
			return t.T("reply.segue.streak", who[0], days)
		},
		func() string {
			day := stats.ComputeToday(players, results, wordle.PuzzleForDate(now))
			if day.Expected() == 0 {
				return ""
			}
			return t.T("reply.segue.today", day.FiledCount(), day.Expected())
		},
	}
	for i := range lines {
		if line := lines[(n+i)%len(lines)](); line != "" {
			return line
		}
	}
	return t.T("reply.segue")
}
