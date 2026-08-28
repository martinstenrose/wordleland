package main

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// bom is the byte-order mark Google Sheets prefixes to CSV exports. Left in
// place it turns the first header into "\ufeffDate", which then matches
// nothing.
const bom = "\ufeff"

// mapping is one row of the mapping file: which column belongs to which
// player, and whether they are still in the group.
type mapping struct {
	column string
	slug   string
	active bool
}

// cell is one player's outcome for one puzzle, before it becomes a result.
type cell struct {
	row      int
	column   string
	puzzleNo int
	date     time.Time
	solved   bool
	guesses  *int
	hardMode bool
}

// parseCell reads one spreadsheet cell.
//
// The format is the share text's own convention, because the source is the
// chat export rather than hand-typed numbers: "4" solved in four, "4*" the
// same in hard mode, "X" failed, "X*" failed in hard mode. Anything else is
// reported rather than guessed at — a cell we cannot read is a scoreline we
// would otherwise invent.
func parseCell(value string) (solved bool, guesses *int, hardMode bool, err error) {
	hardMode = strings.HasSuffix(value, "*")
	token := strings.TrimSuffix(value, "*")

	if strings.EqualFold(token, "X") {
		return false, nil, hardMode, nil
	}

	n, convErr := strconv.Atoi(token)
	if convErr != nil || n < 1 || n > 6 {
		return false, nil, false, fmt.Errorf("%q is not 1-6, X, or either with a trailing * for hard mode", value)
	}
	return true, &n, hardMode, nil
}

func runBackfill(e *env, args []string) error {
	fs := flagSet(e, "backfill")
	file := fs.String("file", "", "`path` to the results CSV exported from the spreadsheet")
	mappingFile := fs.String("mapping", "",
		"`path` to a CSV of column_header,player_slug,active; omit it to read the headers as slugs")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*file, "file"); err != nil {
		return err
	}
	actor, err := e.actor()
	if err != nil {
		return err
	}

	var mappings map[string]mapping
	if *mappingFile != "" {
		mappings, err = readMapping(*mappingFile)
		if err != nil {
			return err
		}
	} else {
		// No mapping file: the headers are the slugs. The second file
		// exists to translate display names and to declare membership; an
		// export whose headers are already slugs needs neither, and
		// insisting on one is a chore with nothing at the end of it.
		mappings, err = mappingFromHeaders(*file)
		if err != nil {
			return err
		}
	}

	cells, problems, err := readResults(*file, mappings)
	if err != nil {
		return err
	}

	// Collect then abort: a real export has more than one problem, and
	// fixing them one run at a time is needless. Nothing is written unless
	// the whole file is sound.
	if len(problems) > 0 {
		fmt.Fprintf(e.out, "%d problem(s) found. Nothing was written.\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(e.out, "  %s\n", p)
		}
		return errors.New("the file did not validate")
	}

	return applyBackfill(e, actor, mappings, cells, *dryRun)
}

// mappingFromHeaders treats each results column as a slug.
//
// active defaults to true, which is the only honest default: the file says
// nothing about membership, and a player being in the export is the best
// evidence available that they are in the group. Declaring otherwise is
// what the mapping file is for.
func mappingFromHeaders(path string) (map[string]mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open results: %w", err)
	}
	defer f.Close()

	header, err := csv.NewReader(f).Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if len(header) < 3 {
		return nil, errors.New("the results file needs Date, Wordle and at least one player column")
	}
	header[0] = strings.TrimPrefix(header[0], bom)

	out := make(map[string]mapping, len(header)-2)
	var bad []string
	for _, raw := range header[2:] {
		column := strings.TrimSpace(raw)
		if column == "" {
			continue
		}
		if !store.ValidSlug(column) {
			bad = append(bad, column)
			continue
		}
		out[column] = mapping{column: column, slug: column, active: true}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf(
			"these column headers are not slugs: %s\n"+
				"either rename them to lowercase letters, digits and hyphens, or pass --mapping",
			strings.Join(bad, ", "))
	}
	if len(out) == 0 {
		return nil, errors.New("the results file has no player columns")
	}
	return out, nil
}

// readMapping loads column_header,player_slug,active.
func readMapping(path string) (map[string]mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mapping: %w", err)
	}
	defer f.Close()

	// LazyQuotes stays off: a quoting mistake should be reported, not guessed
	// at. FieldsPerRecord locks to the header's width, so a short or long row
	// is an error rather than something to pad or truncate.
	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("the mapping file is empty")
	}

	header := records[0]
	header[0] = strings.TrimPrefix(header[0], bom)
	if len(header) < 3 {
		return nil, errors.New("the mapping file needs columns: column_header,player_slug,active")
	}

	mappings := make(map[string]mapping, len(records)-1)
	for i, record := range records[1:] {
		active, err := strconv.ParseBool(strings.TrimSpace(record[2]))
		if err != nil {
			return nil, fmt.Errorf("mapping row %d: active must be true or false, got %q", i+2, record[2])
		}
		column := strings.TrimSpace(record[0])
		mappings[column] = mapping{
			column: column,
			slug:   strings.TrimSpace(record[1]),
			active: active,
		}
	}
	return mappings, nil
}

// readResults parses the results CSV, returning cells and every problem found.
func readResults(path string, mappings map[string]mapping) ([]cell, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open results: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	header[0] = strings.TrimPrefix(header[0], bom)

	if len(header) < 3 || !strings.EqualFold(header[0], "Date") || !strings.EqualFold(header[1], "Wordle") {
		return nil, nil, fmt.Errorf("the first two columns must be Date and Wordle, got %q and %q",
			header[0], header[1])
	}

	// Both directions are checked, because they are the same mistake seen from
	// either end: a typo in the mapping produces an unmapped column and an
	// unmatched mapping row at once. Reporting only the first would leave the
	// other to be found by accident, or not at all if the typo happens to be
	// in a column the results file no longer contains.
	var problems []string
	seen := make(map[string]bool, len(header)-2)
	for _, column := range header[2:] {
		column = strings.TrimSpace(column)
		seen[column] = true
		if _, ok := mappings[column]; !ok {
			// Aborting rather than skipping: a skipped column silently drops
			// that player's entire history.
			problems = append(problems, fmt.Sprintf(
				"column %q has no mapping row; add it to the mapping file or remove the column", column))
		}
	}
	for column := range mappings {
		if !seen[column] {
			problems = append(problems, fmt.Sprintf(
				"mapping row %q matches no column in the results file; check it for a typo", column))
		}
	}
	sort.Strings(problems)

	var cells []cell
	for row := 2; ; row++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("row %d: %v", row, err))
			// A structural error means every later row is suspect too.
			break
		}

		puzzleNo, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(record[1]), ",", ""))
		if err != nil {
			problems = append(problems, fmt.Sprintf("row %d: Wordle %q is not a number", row, record[1]))
			continue
		}
		derived, err := wordle.DateForPuzzle(puzzleNo)
		if err != nil {
			problems = append(problems, fmt.Sprintf("row %d: %v", row, err))
			continue
		}

		// The file's own Date column is checked against the derived one
		// rather than either being trusted silently.
		stated := strings.TrimSpace(record[0])
		if stated != "" && stated != derived.Format(time.DateOnly) {
			problems = append(problems, fmt.Sprintf(
				"row %d: puzzle %d falls on %s, but the file says %s",
				row, puzzleNo, derived.Format(time.DateOnly), stated))
			continue
		}

		for i, raw := range record[2:] {
			if i+2 >= len(header) {
				break
			}
			column := strings.TrimSpace(header[i+2])
			value := strings.TrimSpace(raw)
			if value == "" {
				// Empty means did not play, which is the absence of a row
				// rather than a row full of nulls.
				continue
			}

			solved, guesses, hardMode, err := parseCell(value)
			if err != nil {
				problems = append(problems, fmt.Sprintf("row %d, column %q: %v", row, column, err))
				continue
			}
			cells = append(cells, cell{
				row: row, column: column, puzzleNo: puzzleNo, date: derived,
				solved: solved, guesses: guesses, hardMode: hardMode,
			})
		}
	}
	return cells, problems, nil
}

// applyBackfill writes the parsed cells.
func applyBackfill(e *env, actor store.Actor, mappings map[string]mapping, cells []cell, dryRun bool) error {
	var created, inserted, updated, unchanged, activeChanged int

	err := store.InTx(e.ctx, e.db, func(tx *sql.Tx) error {
		players := make(map[string]store.Player, len(mappings))

		for column, m := range mappings {
			player, err := store.PlayerBySlug(e.ctx, tx, m.slug)
			isNew := errors.Is(err, store.ErrPlayerNotFound)
			switch {
			case isNew:
				if dryRun {
					players[column] = store.Player{Slug: m.slug}
					created++
					continue
				}
				player, err = store.CreatePlayerTx(e.ctx, tx, actor, m.column, m.slug)
				if err != nil {
					return fmt.Errorf("create player %s: %w", m.slug, err)
				}
				created++
			case err != nil:
				return err
			}

			// The mapping declares membership and a re-run applies it.
			// Backfill is an import, not a sync: once the live roster has
			// moved on, re-running will resurrect anyone since retired.
			//
			// A player created moments ago is not a membership change. Setting
			// their flag is part of creating them, so it is applied but not
			// counted — otherwise a first import reports having changed the
			// membership of people who did not exist before it ran.
			if player.ID != 0 && player.Active != m.active {
				if !dryRun {
					if _, err := store.UpdatePlayerTx(e.ctx, tx, actor, player.ID,
						store.PlayerUpdate{Active: &m.active}); err != nil {
						return err
					}
				}
				if !isNew {
					activeChanged++
				}
			}
			players[column] = player
		}

		for _, c := range cells {
			player, ok := players[c.column]
			if !ok || player.ID == 0 {
				// Only reachable in a dry run, where a player that would be
				// created has no id yet.
				inserted++
				continue
			}

			result := store.Result{
				PuzzleNo: c.puzzleNo,
				Date:     c.date,
				PlayerID: player.ID,
				Guesses:  c.guesses,
				Solved:   c.solved,
				HardMode: c.hardMode,
			}

			existing, err := store.ResultFor(e.ctx, tx, c.puzzleNo, player.ID)
			switch {
			case errors.Is(err, store.ErrResultNotFound):
				inserted++
			case err != nil:
				return err
			case sameResult(existing, result):
				unchanged++
				continue
			default:
				updated++
			}

			if dryRun {
				continue
			}

			// entered_by is the acting admin, never NULL. The sheet is
			// hand-curated and contains corrections; left NULL, replaying old
			// Signal history through the ingest API would silently revert
			// every one of them.
			if _, _, err := store.UpsertResult(e.ctx, tx, result, actor.UserID); err != nil {
				return err
			}
		}

		if dryRun {
			// Rolled back rather than committed, so a dry run cannot leave
			// anything behind even if a write slipped through above.
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return err
	}

	prefix := ""
	if dryRun {
		prefix = "Would have "
	}
	fmt.Fprintf(e.out, "%sread %d cell(s).\n", prefix, len(cells))
	fmt.Fprintf(e.out, "  players created:  %d\n", created)
	fmt.Fprintf(e.out, "  results inserted: %d\n", inserted)
	fmt.Fprintf(e.out, "  results updated:  %d\n", updated)
	fmt.Fprintf(e.out, "  results unchanged:%d\n", unchanged)
	if activeChanged > 0 {
		fmt.Fprintf(e.out, "  membership changed: %d\n", activeChanged)
		fmt.Fprintf(e.out, "\nNote: the mapping file's active column changed membership for %d player(s)\n"+
			"who already existed. Backfill is an import rather than a sync, so a re-run\n"+
			"reapplies the file and will undo any retirement made since.\n", activeChanged)
	}
	if dryRun {
		fmt.Fprintln(e.out, "\nNothing was written. Re-run without --dry-run to apply.")
	}
	return nil
}

// errDryRun rolls the transaction back at the end of a dry run.
var errDryRun = errors.New("dry run")

func sameResult(existing *store.Result, want store.Result) bool {
	if existing.Solved != want.Solved || existing.HardMode != want.HardMode {
		return false
	}
	if (existing.Guesses == nil) != (want.Guesses == nil) {
		return false
	}
	return existing.Guesses == nil || *existing.Guesses == *want.Guesses
}
