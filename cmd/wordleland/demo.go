package main

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/demo"
	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// pendingSenders is how many unclaimed senders `demo seed` holds results
// for, so the Pending admin screen has something to show. Fixed rather than
// randomised: the brief's "2-3" is about not making it look too tidy, not
// about being unpredictable from one seed to the next.
const pendingSenders = 3

// runDemo gates the whole noun on DEMO_MODE.
//
// A verb that deletes and invents players must not be reachable by an
// operator who forgot a flag on a real deployment; checking it in one place
// here, rather than in each subcommand, is what makes that true regardless
// of which one they typed.
func runDemo(e *env, args []string) error {
	on, err := config.DemoMode()
	if err != nil {
		return err
	}
	if !on {
		return errors.New("the demo verb is disabled: set DEMO_MODE=true to use it. " +
			"It generates and deletes players and must never be set on a production instance")
	}
	return dispatch(e, "demo", []subcommand{
		{"seed", "create a synthetic roster and backfill its history", demoSeed},
		{"tick", "file today's result for each active synthetic player", demoTick},
		{"clear", "remove every player and their data (dry run unless --apply)", demoClear},
	}, args)
}

func demoSeed(e *env, args []string) error {
	fs := flagSet(e, "demo seed")
	players := fs.Int("players", 12, "number of synthetic players to create")
	days := fs.Int("days", 200, "number of days of history to backfill")
	seed := fs.Int64("seed", 0, "random seed; omit for a different roster and history each run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *players <= 0 {
		return errors.New("--players must be positive")
	}
	if *days <= stats.AbsentDays {
		return fmt.Errorf("--days must be greater than %d: the reserved \"Missing\" persona needs "+
			"at least that many trailing days with nobody playing to demonstrate the callout it exists for",
			stats.AbsentDays)
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	fmt.Fprintf(e.out, "Using seed %d (pass --seed %d to reproduce this run).\n", *seed, *seed)

	// The roster is sized for the real players plus a few names held back
	// for unclaimed senders, so neither pool draws on the other and a
	// pending sender's display name looks exactly like a player's.
	roster, err := demo.NewRoster(*seed, *players+pendingSenders)
	if err != nil {
		return err
	}
	playerPersonas, senderPersonas := roster[:*players], roster[*players:]

	today := wordle.PuzzleForDate(time.Now())
	oldest := today - (*days - 1)

	rng := rand.New(rand.NewSource(*seed))

	var resultsFiled int
	var retiredSlug string
	for _, persona := range playerPersonas {
		player, err := store.CreatePlayer(e.ctx, e.db, actor, persona.Name, "")
		if err != nil {
			return fmt.Errorf("create %s: %w", persona.Name, err)
		}

		for day := 0; day < *days; day++ {
			if !persona.Played(rng, day, *days) {
				continue
			}
			outcome := persona.Play(rng)
			result, err := ingest.Apply(e.ctx, e.db, actor, submissionFor(player.Slug, oldest+day, outcome), false)
			if err != nil {
				return fmt.Errorf("file result for %s, puzzle %d: %w", player.Slug, oldest+day, err)
			}
			if result.Status == ingest.StatusCreated {
				resultsFiled++
			}
		}

		if persona.Role == demo.RoleRetired {
			inactive := false
			if _, err := store.UpdatePlayer(e.ctx, e.db, actor, player.ID, store.PlayerUpdate{Active: &inactive}); err != nil {
				return fmt.Errorf("retire %s: %w", player.Slug, err)
			}
			retiredSlug = player.Slug
		}
	}

	var resultsPending int
	for i, persona := range senderPersonas {
		externalID := fmt.Sprintf("demo-sender-%d", i+1)
		// Within the last few days, so the sender looks like someone who
		// just posted rather than an artifact from the start of history.
		puzzleNo := today - rng.Intn(3)
		outcome := persona.Play(rng)
		sub := submissionFor("", puzzleNo, outcome)
		sub.Source, sub.ExternalID, sub.DisplayHint = "signal", externalID, persona.Name

		result, err := ingest.Apply(e.ctx, e.db, actor, sub, false)
		if err != nil {
			return fmt.Errorf("hold result for sender %s: %w", externalID, err)
		}
		if result.Status == ingest.StatusPending {
			resultsPending++
		}
	}

	fmt.Fprintf(e.out, "Seeded %d player(s) over %d day(s).\n", *players, *days)
	fmt.Fprintf(e.out, "  results filed:      %d\n", resultsFiled)
	fmt.Fprintf(e.out, "  senders held:       %d\n", resultsPending)
	if retiredSlug != "" {
		fmt.Fprintf(e.out, "  retired player:     %s\n", retiredSlug)
	}
	return nil
}

// submissionFor builds the ingest submission for one day's outcome. Slug
// names the player directly for a backfilled or ticked result; leave it
// empty and set Source/ExternalID/DisplayHint instead for a held sender.
func submissionFor(slug string, puzzleNo int, outcome demo.Outcome) ingest.Submission {
	var guesses *int
	if outcome.Solved {
		g := outcome.Guesses
		guesses = &g
	}
	return ingest.Submission{
		Slug:     slug,
		PuzzleNo: puzzleNo,
		Solved:   outcome.Solved,
		Guesses:  guesses,
		HardMode: outcome.HardMode,
		Via:      "demo",
	}
}

func demoTick(e *env, args []string) error {
	fs := flagSet(e, "demo tick")
	seed := fs.Int64("seed", 0,
		"salt for tests that need a different simulated puzzle; leave at 0 for a real deployment")
	if err := fs.Parse(args); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	today := wordle.PuzzleForDate(time.Now())

	players, err := store.ListPlayers(e.ctx, e.db)
	if err != nil {
		return err
	}

	var filed, already, satOut int
	for _, player := range players {
		// A retired player stays retired. tick reads that from the player
		// itself rather than tracking who was ever synthetic, which is the
		// same reasoning demo clear uses: on a DEMO_MODE instance there is
		// no other kind of player.
		if !player.Active {
			continue
		}

		// Skips the roll entirely for a player already filed, so a repeat
		// run does not spam the audit log with identical "updated" entries
		// — UpsertResult does not check whether the value actually changed.
		_, err := store.ResultFor(e.ctx, e.db, today, player.ID)
		if err == nil {
			already++
			continue
		}
		if !errors.Is(err, store.ErrResultNotFound) {
			return err
		}

		// Seeded from the player and the puzzle, not the time tick happens
		// to run: sitting a day out leaves no row to check against, so a
		// second invocation for the same puzzle must reroll to exactly the
		// same decision rather than a fresh one, or a retry could go from
		// "sat out" to "played" on nothing but bad timing.
		persona := demo.PersonaFor(player.Name)
		rng := demo.DailyRNG(player.Name, today, *seed)
		if rng.Float64() < persona.MissRate {
			satOut++
			continue
		}

		outcome := persona.Play(rng)
		result, err := ingest.Apply(e.ctx, e.db, actor, submissionFor(player.Slug, today, outcome), false)
		if err != nil {
			return fmt.Errorf("file result for %s: %w", player.Slug, err)
		}
		if result.Status == ingest.StatusCreated {
			filed++
		}
	}

	fmt.Fprintf(e.out, "Puzzle #%d: filed %d result(s), %d already had one, %d sat it out.\n",
		today, filed, already, satOut)
	return nil
}

func demoClear(e *env, args []string) error {
	fs := flagSet(e, "demo clear")
	apply := fs.Bool("apply", false, "actually delete; without this, only report what would happen")
	if err := fs.Parse(args); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	summary, err := store.ClearDemoData(e.ctx, e.db, actor, !*apply)
	if err != nil {
		return err
	}

	prefix := ""
	if !*apply {
		prefix = "Would have "
	}
	fmt.Fprintf(e.out, "%sdeleted %d player(s) and %d held pending result(s).\n",
		prefix, summary.Deleted, summary.PendingCleared)
	if len(summary.Blocked) > 0 {
		fmt.Fprintf(e.out, "  left in place, blocked by a pending invitation: %s\n",
			strings.Join(summary.Blocked, ", "))
	}
	if !*apply {
		fmt.Fprintln(e.out, "\nNothing was written. Re-run with --apply to actually delete.")
	}
	return nil
}
