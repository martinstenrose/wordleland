package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Fixtures are inline rather than files under testdata: .gitignore excludes
// *.csv so the real export can sit in the project root untracked, and a
// committed fixture would need an exception. Keeping them here also puts each
// one next to the assertions that use it.
const (
	mappingCSV = `column_header,player_slug,active
Martin,martin,true
Alex,alex,true
Sam,sam,false
`

	// Dates match what DateForPuzzle derives, filled in by rewriteDates.
	resultsCSV = `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,4,X,3
DATE_1889,1889,3,5,
DATE_1890,1890,,2,6
`

	// The Date column disagrees with the puzzle number on the second row.
	mismatchCSV = `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,4,X,3
2020-01-01,1889,3,5,
`

	// Sam has no mapping row.
	unmappedCSV = `Date,Wordle,Martin,Nobody
DATE_1888,1888,4,3
`

	badCellCSV = `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,9,X,3
DATE_1889,1889,3,banana,
`
)

// rewriteDates fills DATE_<puzzle> placeholders with the derived date, so the
// fixture cannot drift from the epoch.
func rewriteDates(t *testing.T, content string) string {
	t.Helper()
	for _, puzzle := range []int{1888, 1889, 1890} {
		date, err := wordle.DateForPuzzle(puzzle)
		if err != nil {
			t.Fatalf("DateForPuzzle(%d) failed: %v", puzzle, err)
		}
		content = strings.ReplaceAll(content,
			"DATE_"+itoa(puzzle), date.Format("2006-01-02"))
	}
	return content
}

func itoa(n int) string {
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// writeFixture writes content to a temporary file and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// backfillCLI returns a CLI with an admin already created.
func backfillCLI(t *testing.T) *cli {
	t.Helper()
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	return c
}

func TestBackfillImports(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	if !strings.Contains(out, "players created:  3") {
		t.Errorf("output does not report three players created:\n%s", out)
	}

	ctx := context.Background()
	db := c.db()

	// Every non-empty cell became a row; empty ones did not.
	martin, err := store.PlayerBySlug(ctx, db, "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if _, err := store.ResultFor(ctx, db, 1890, martin.ID); err == nil {
		t.Error("an empty cell produced a row; a missed day is the absence of one")
	}

	solved, err := store.ResultFor(ctx, db, 1888, martin.ID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if !solved.Solved || *solved.Guesses != 4 {
		t.Errorf("result = %+v, want four guesses", solved)
	}
	// entered_by is the acting admin, which is what stops a later token write
	// reverting the corrections the sheet contains.
	if solved.EnteredBy == nil {
		t.Error("imported rows have entered_by NULL; a token write could revert them")
	}
	// The sheet holds no hard-mode data, so every row records false.
	if solved.HardMode {
		t.Error("hard_mode was set on an imported row")
	}

	alex, err := store.PlayerBySlug(ctx, db, "alex")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	failed, err := store.ResultFor(ctx, db, 1888, alex.ID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if failed.Solved || failed.Guesses != nil {
		t.Errorf("X became %+v, want a failure with no guess count", failed)
	}
}

// The mapping declares membership, and it is applied on import.
func TestBackfillAppliesActiveFromMapping(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	sam, err := store.PlayerBySlug(context.Background(), c.db(), "sam")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if sam.Active {
		t.Error("Sam is active, but the mapping marks them as having left")
	}
}

// Backfill is an import, not a sync: re-running after the roster has moved on
// resurrects anyone since retired. Pinned because it is a real hazard rather
// than a bug.
func TestBackfillRerunReappliesActive(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)
	ctx := context.Background()

	c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	// An admin retires Martin after the import.
	c.mustRun("", "--as", "admin@example.tld", "player", "update", "--player", "martin", "--active=false")

	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	martin, err := store.PlayerBySlug(ctx, c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if !martin.Active {
		t.Error("the re-run did not reapply the mapping's active value")
	}
	if !strings.Contains(out, "membership changed") {
		t.Errorf("the output does not flag that membership was changed:\n%s", out)
	}
}

// Idempotent: a second run over the same file changes nothing.
func TestBackfillIsIdempotent(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	if !strings.Contains(out, "results inserted: 0") {
		t.Errorf("the re-run inserted rows:\n%s", out)
	}
	if !strings.Contains(out, "results updated:  0") {
		t.Errorf("the re-run updated rows:\n%s", out)
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping, "--dry-run")

	if !strings.Contains(out, "Nothing was written") {
		t.Errorf("the dry run does not say it wrote nothing:\n%s", out)
	}

	var players, results2 int
	db := c.db()
	if err := db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&players); err != nil {
		t.Fatalf("count players: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&results2); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if players != 0 || results2 != 0 {
		t.Errorf("the dry run wrote %d players and %d results", players, results2)
	}
}

// Collect then abort, with zero partial writes: a real export has more than
// one problem, and fixing them one run at a time is needless.
func TestBackfillAbortsOnDateMismatch(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, mismatchCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	if err == nil {
		t.Fatal("the mismatched file was imported")
	}
	if !strings.Contains(out, "falls on") {
		t.Errorf("the report does not explain the mismatch:\n%s", out)
	}

	var players int
	if err := c.db().QueryRow(`SELECT COUNT(*) FROM players`).Scan(&players); err != nil {
		t.Fatalf("count players: %v", err)
	}
	if players != 0 {
		t.Errorf("players created = %d despite the abort, want 0", players)
	}
}

// Skipping an unmapped column would silently drop that player's entire
// history, so it aborts instead.
func TestBackfillAbortsOnUnmappedColumn(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, unmappedCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	if err == nil {
		t.Fatal("a file with an unmapped column was imported")
	}
	if !strings.Contains(out, "no mapping") {
		t.Errorf("the report does not name the unmapped column:\n%s", out)
	}
}

func TestBackfillReportsEveryBadCell(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, badCellCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	if err == nil {
		t.Fatal("a file with unparseable cells was imported")
	}
	// Both problems, not just the first.
	if !strings.Contains(out, `"9"`) || !strings.Contains(out, `"banana"`) {
		t.Errorf("the report stops at the first bad cell:\n%s", out)
	}
}

// Sheets prefixes a BOM, which left in place turns the first header into
// something that matches nothing.
func TestBackfillStripsBOM(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", "\ufeff"+rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", "\ufeff"+mappingCSV)

	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	if !strings.Contains(out, "players created:  3") {
		t.Errorf("the BOM broke the import:\n%s", out)
	}
}

// A quoting mistake is reported rather than guessed at, since LazyQuotes off
// is what makes a shifted column impossible.
func TestBackfillRejectsRaggedRows(t *testing.T) {
	c := backfillCLI(t)
	ragged := "Date,Wordle,Martin,Alex,Sam\n" +
		"DATE_1888,1888,4,X,3\n" +
		"DATE_1889,1889,3\n"
	results := writeFixture(t, "results.csv", rewriteDates(t, ragged))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	if _, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping); err == nil {
		t.Fatal("a short row was accepted")
	}
}

// The cell format is the share text's own convention, because the source is
// the chat export rather than hand-typed numbers.
const hardModeCSV = `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,4*,X*,3
DATE_1889,1889,3,5*,
DATE_1890,1890,,2,6*
`

func TestBackfillParsesHardMode(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, hardModeCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	ctx := context.Background()
	db := c.db()
	martin, err := store.PlayerBySlug(ctx, db, "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	alex, err := store.PlayerBySlug(ctx, db, "alex")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}

	tests := []struct {
		name     string
		playerID int64
		puzzle   int
		solved   bool
		guesses  int
		hardMode bool
	}{
		{"solved in hard mode", martin.ID, 1888, true, 4, true},
		{"solved normally", martin.ID, 1889, true, 3, false},
		{"failed in hard mode", alex.ID, 1888, false, 0, true},
		{"solved in hard mode, other player", alex.ID, 1889, true, 5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ResultFor(ctx, db, tt.puzzle, tt.playerID)
			if err != nil {
				t.Fatalf("ResultFor() failed: %v", err)
			}
			if got.Solved != tt.solved {
				t.Errorf("Solved = %v, want %v", got.Solved, tt.solved)
			}
			if tt.solved && *got.Guesses != tt.guesses {
				t.Errorf("Guesses = %d, want %d", *got.Guesses, tt.guesses)
			}
			if !tt.solved && got.Guesses != nil {
				t.Errorf("Guesses = %v for a failure, want nil", got.Guesses)
			}
			if got.HardMode != tt.hardMode {
				t.Errorf("HardMode = %v, want %v", got.HardMode, tt.hardMode)
			}
		})
	}
}

// A cell we cannot read is a scoreline we would otherwise invent, so an
// unrecognised suffix is reported rather than guessed at.
func TestBackfillRejectsUnknownCellSuffix(t *testing.T) {
	c := backfillCLI(t)
	bad := `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,4?,X,3
DATE_1889,1889,3**,5,
`
	results := writeFixture(t, "results.csv", rewriteDates(t, bad))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	if err == nil {
		t.Fatal("cells with an unknown suffix were imported")
	}
	for _, want := range []string{`"4?"`, `"3**"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %s:\n%s", want, out)
		}
	}
}

// Re-importing with hard mode present exercises the update path, which a
// first import never does.
func TestBackfillUpdatesExistingRows(t *testing.T) {
	c := backfillCLI(t)
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	plain := writeFixture(t, "plain.csv", rewriteDates(t, resultsCSV))
	c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", plain, "--mapping", mapping)

	// The same scores, now carrying hard mode.
	withHard := writeFixture(t, "hard.csv", rewriteDates(t, `Date,Wordle,Martin,Alex,Sam
DATE_1888,1888,4*,X,3
DATE_1889,1889,3,5,
DATE_1890,1890,,2,6
`))
	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", withHard, "--mapping", mapping)

	if !strings.Contains(out, "results updated:  1") {
		t.Errorf("the changed row was not reported as updated:\n%s", out)
	}
	if !strings.Contains(out, "results inserted: 0") {
		t.Errorf("the re-run inserted rows:\n%s", out)
	}

	martin, err := store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	got, err := store.ResultFor(context.Background(), c.db(), 1888, martin.ID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if !got.HardMode {
		t.Error("the update did not carry hard mode through")
	}
}

// A typo in the mapping produces an unmatched mapping row, which is the same
// mistake as an unmapped column seen from the other end.
func TestBackfillAbortsOnUnmatchedMappingRow(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	// "Samuel" matches no column; the results file has "Sam".
	mapping := writeFixture(t, "mapping.csv", `column_header,player_slug,active
Martin,martin,true
Alex,alex,true
Samuel,sam,false
`)

	out, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)
	if err == nil {
		t.Fatal("a mapping row matching no column was accepted")
	}
	// Both ends of the same typo are reported together, so it cannot be
	// half-fixed and re-run.
	if !strings.Contains(out, `"Samuel" matches no column`) {
		t.Errorf("the unmatched mapping row was not reported:\n%s", out)
	}
	if !strings.Contains(out, `"Sam" has no mapping row`) {
		t.Errorf("the unmapped column was not reported alongside it:\n%s", out)
	}
}

// A player created by this run must come out with the mapping's active value
// already applied, and must not be counted as a membership change: setting the
// flag is part of creating them, not a change to them.
//
// Regression guard for a report that a player marked false in the mapping came
// out active after a first import. It has never reproduced here, on this code
// or on the version that report was made against, so this test exists to catch
// it if it ever does.
func TestBackfillDoesNotReportMembershipChangeForNewPlayers(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	out := c.mustRun("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping)

	if strings.Contains(out, "membership changed") {
		t.Errorf("a first import reported changing membership for players it created:\n%s", out)
	}

	// The flag was still applied, just not counted as a change.
	sam, err := store.PlayerBySlug(context.Background(), c.db(), "sam")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if sam.Active {
		t.Error("the mapping's active value was not applied to a player created by the same run")
	}

	// Players the mapping marks true are unaffected.
	martin, err := store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if !martin.Active {
		t.Error("a player the mapping marks active was created inactive")
	}
}

// Every CLI write takes --as, and the missing-admin error advises it.
// It must therefore parse where someone would naturally type it.
func TestAsFlagIsGlobal(t *testing.T) {
	c := backfillCLI(t)
	results := writeFixture(t, "results.csv", rewriteDates(t, resultsCSV))
	mapping := writeFixture(t, "mapping.csv", mappingCSV)

	// Before the noun, which is where the error message points.
	if _, err := c.run("", "--as", "admin@example.tld", "backfill", "--file", results, "--mapping", mapping); err != nil {
		t.Errorf("--as before the noun failed: %v", err)
	}
}

// Who is acting is a property of the invocation rather than of any one
// verb, so it reads the same way whichever verb follows.
func TestAsFlagIsGlobalForEveryCommand(t *testing.T) {
	c := backfillCLI(t)

	// A representative write from each noun.
	for _, args := range [][]string{
		{"--as", "admin@example.tld", "player", "add", "--name", "Martin"},
		{"--as", "admin@example.tld", "player", "update", "--player", "martin", "--name", "Martin S"},
		{"--as", "admin@example.tld", "token", "create", "--label", "import-script"},
		{"--as", "admin@example.tld", "user", "disable", "--email", "admin@example.tld"},
	} {
		if _, err := c.run("", args...); err != nil {
			t.Errorf("%v failed: %v", args, err)
		}
	}
}
