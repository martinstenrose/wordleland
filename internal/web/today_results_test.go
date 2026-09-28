package web

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// resultRows reads the day's list off the page: position, score, name, delta.
func resultRows(t *testing.T, body string) [][4]string {
	t.Helper()
	row := regexp.MustCompile(`(?s)<li class="result-row">\s*` +
		`<span class="result-pos num">([^<]*)</span>\s*` +
		`<span class="cell t\d tiny result-score">([^<]*)</span>\s*` +
		`<span class="result-id">\s*<a class="player result-name" href="[^"]+">([^<]+)</a>.*?` +
		`<span class="result-delta delta [a-z]+">([^<]*)</span>`)
	var out [][4]string
	for _, m := range row.FindAllStringSubmatch(body, -1) {
		out = append(out, [4]string{m[1], m[2], m[3], strings.TrimSpace(m[4])})
	}
	return out
}

// The day used to be a wrapping strip of name-and-score pairs. It said who
// had played and nothing else: not who was ahead, and not whether a 4 was a
// good day for that person or a bad one. Both are answered by figures the
// page already had.
func TestTodayListsTheDayBestFirstWithEachPlayersOwnDelta(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	got := resultRows(t, fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String())

	// seedBoard's regulars each score their own average every day, so the
	// order here is the scores and the deltas are all nothing. Thin has too
	// few games to be ranked and is in the day like everybody else.
	want := [][4]string{
		{"1", "2", "Normalb", "±0.00"},
		{"2", "3*", "Harda", "±0.00"},
		{"—", "3", "Thin", ""},
		{"3", "4*", "Hardb", "±0.00"},
		{"4", "5", "Normala", "±0.00"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// A position among ranked players is not a thing an unranked player holds,
// and numbering them would push everyone below them down a place for the
// wrong reason. They are still in the day, which is a fact about the day
// rather than about the board.
func TestAnUnrankedPlayerTakesNoPositionFromAnyoneElse(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	for _, row := range resultRows(t, body) {
		if row[2] != "Thin" {
			continue
		}
		if row[0] != "—" {
			t.Errorf("an unranked player holds position %q", row[0])
		}
		// Their figure is withheld here as it is everywhere else: a dash,
		// which opens why. A count in the column wrapped to two lines.
		if !strings.Contains(body, `<summary class="num" aria-label="Not ranked yet: 4 puzzles played. The board ranks from 10.">—</summary>`) {
			t.Error("the unranked row is not a dash that says why")
		}
		return
	}
	t.Fatal("the unranked player is not in the day at all")
}

// A miss has no distance from an average: it is off the scale the average is
// measured on, and "▲ 2.61" would invent one.
func TestAMissSaysSoRatherThanInventingADistance(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()

	// One regular who fails today, one who has a better day than usual.
	failed, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Failed", "failed")
	if err != nil {
		t.Fatal(err)
	}
	sharp, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Sharp", "sharp")
	if err != nil {
		t.Fatal(err)
	}
	for puzzle := current - 20; puzzle < current; puzzle++ {
		seedResult(t, srv, failed.ID, puzzle, 4, false)
		seedResult(t, srv, sharp.ID, puzzle, 4, false)
	}
	seedResult(t, srv, failed.ID, current, 0, false)
	seedResult(t, srv, sharp.ID, current, 2, false)

	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	rows := resultRows(t, fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String())

	got := map[string][4]string{}
	for _, row := range rows {
		got[row[2]] = row
	}
	if got["Failed"][1] != "X" || got["Failed"][3] != "missed" {
		t.Errorf("a miss reads %v, want an X and \"missed\"", got["Failed"])
	}
	// And a good day is measured, because there is something to measure.
	if got["Sharp"][3] != "▼ 1.90" {
		t.Errorf("a better-than-usual day reads %q, want the distance from their own average", got["Sharp"][3])
	}
	// Best first: the solve leads the day, the miss ends it.
	if rows[0][2] != "Sharp" || rows[len(rows)-1][2] != "Failed" {
		t.Errorf("the day is not ordered best first: %v", rows)
	}
}

// How far through the day the group is, under the day's result. The names of
// whoever is left are worth a count all day and worth reading only when
// somebody asks, so the count opens them — but they are in the markup either
// way, which is one round trip and one script fewer than fetching them.
func TestTheDaysProgressIsAFigureAndTheNamesAreADisclosure(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()

	// Five of the six filed; lapsed did not.
	if !strings.Contains(body, `aria-label="5 of 6 results in"`) {
		t.Error("the progress track does not say the count as a sentence")
	}
	if !strings.Contains(body, `--pct: 83`) {
		t.Error("the progress track is not filled to the share that has filed")
	}
	out, ok := sectionOf(body, `<details class="today-out">`, "</details>")
	if !ok {
		t.Fatal("the count does not open who is still out")
	}
	if !strings.Contains(out, `<summary><span class="num">5 of 6 in</span>`) {
		t.Error("the count is not printed beside the track")
	}
	if !strings.Contains(out, ">Lapsed<") {
		t.Error("the name is not inside the disclosure")
	}
	if strings.Contains(out, "benched=") || linkWithPartial.MatchString(body) {
		t.Error("the names are fetched rather than rendered")
	}
}

// Before today's first result lands there is no day to show: no winner, no
// spread, no list of scores. The page says so in one card, where the day will
// be, rather than drawing each of those empty — and the form list, which is
// about the last thirty days and not today, stays.
func TestTheResultsTableHasAPlaceholderBeforeAnyoneFiles(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.AdminActor(admin.ID)
	current := currentPuzzle()

	// Ranked, with history — but none of it reaches today.
	for _, slug := range []string{"morning", "evening"} {
		p, err := store.CreatePlayer(ctx, srv.db, actor, strings.ToUpper(slug[:1])+slug[1:], slug)
		if err != nil {
			t.Fatal(err)
		}
		for puzzle := current - 25; puzzle < current; puzzle++ {
			seedResult(t, srv, p.ID, puzzle, 3, false)
		}
	}

	body := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()

	if !strings.Contains(body, `<h2 class="page-empty-title">Nobody has played yet today.</h2>`) {
		t.Error("the day does not say it is empty")
	}
	if !strings.Contains(body, `<p class="page-sub">Nobody has played yet.</p>`) {
		t.Error("the page head does not say nobody has played")
	}
	for _, gone := range []string{`class="card today-hero"`, `today-results"`, `class="today-out"`, `class="today-tabs"`} {
		if strings.Contains(body, gone) {
			t.Errorf("an empty day still draws %s", gone)
		}
	}
	if !strings.Contains(body, `today-form"`) {
		t.Error("the form list went with the day's results")
	}
}

// With nobody on the board at all, Today is the empty card and nothing else:
// no lists to leave empty and no callouts to show.
func TestTodayOnAnEmptyBoardIsOneEmptyCard(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	admin, err := store.CreateUser(context.Background(), srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	body := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, `<h2 class="page-empty-title">Nobody has played yet today.</h2>`) {
		t.Error("the empty day does not say so")
	}
	for _, gone := range []string{`today-results"`, `today-form"`, `class="today-callouts"`, `class="card today-standing"`} {
		if strings.Contains(body, gone) {
			t.Errorf("an empty board still draws %s", gone)
		}
	}
}

// Everyone the board does not rank, behind a disclosure for the same reason:
// the front page is about who is playing, and this list grows forever as
// people drift away.
func TestTheUnrankedAreBehindADisclosureWithTheirReason(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	// To the end of the row list rather than to the first </details>: every
	// row carries a reason chip, which is a <details> of its own.
	bench := body[strings.Index(body, `<details class="today-bench">`):]
	bench = bench[:strings.Index(bench, "</ol>")]

	if !strings.Contains(bench, "2 players not ranked") {
		t.Error("the disclosure does not count the unranked")
	}
	for _, name := range []string{">Thin<", ">Lapsed<"} {
		if !strings.Contains(bench, name) {
			t.Errorf("%s is not in the unranked list", name)
		}
	}
	// Why, not just that: the two are unranked for different reasons.
	if !strings.Contains(bench, "no recent puzzles") || !strings.Contains(bench, "low data") {
		t.Errorf("the unranked rows do not say why: %s", bench)
	}
}

// Hard mode leads a tie and only a tie. The names are chosen so the two
// rules disagree: Quick sorts before Zora alphabetically, so Zora leading
// can only be the hard-mode rule — and Quick's ordinary 3 still beats
// Easy's 4 played hard, because hard mode orders equal results rather than
// discounting unequal ones.
func TestHardModeLeadsATiedScoreButNeverBeatsABetterOne(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()

	for _, p := range []struct {
		name, slug string
		hardMode   bool
		today      int
	}{
		{"Quick", "quick", false, 3},
		{"Zora", "zora", true, 3},
		{"Easy", "easy", true, 4},
	} {
		player, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), p.name, p.slug)
		if err != nil {
			t.Fatal(err)
		}
		for puzzle := current - 20; puzzle < current; puzzle++ {
			seedResult(t, srv, player.ID, puzzle, 4, p.hardMode)
		}
		seedResult(t, srv, player.ID, current, p.today, p.hardMode)
	}

	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	rows := resultRows(t, fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String())

	var got [][3]string
	for _, row := range rows {
		got = append(got, [3]string{row[0], row[1], row[2]})
	}
	want := [][3]string{
		{"1", "3*", "Zora"},
		{"2", "3", "Quick"},
		{"3", "4*", "Easy"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}
