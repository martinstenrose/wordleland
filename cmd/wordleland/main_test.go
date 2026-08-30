package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"errors"
	"flag"
	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/version"
)

// cli runs the CLI against a temporary database, returning its output.
type cli struct {
	t      *testing.T
	dbPath string
}

func newCLI(t *testing.T) *cli {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// serve owns migrations; the CLI only ever runs against a
	// database it has already prepared.
	if err := store.Migrate(context.Background(), db, store.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Close()

	return &cli{t: t, dbPath: dbPath}
}

// run invokes the CLI. Password prompts read from stdin, which is not a
// terminal under test, so a password is supplied as a single line.
func (c *cli) run(stdin string, args ...string) (string, error) {
	c.t.Helper()

	if stdin != "" {
		defer swapStdin(c.t, stdin)()
	}

	var out bytes.Buffer
	err := run(append([]string{"-db", c.dbPath}, args...), &out)
	return out.String(), err
}

// mustRun fails the test if the command errors.
func (c *cli) mustRun(stdin string, args ...string) string {
	c.t.Helper()
	out, err := c.run(stdin, args...)
	if err != nil {
		c.t.Fatalf("wordleland %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// db opens the test database for assertions.
func (c *cli) db() *sql.DB { return openForAssert(c.t, c.dbPath) }

func TestCLIRequiresACommand(t *testing.T) {
	c := newCLI(t)

	out, err := c.run("")
	if err == nil {
		t.Fatal("running with no command succeeded")
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("output does not show usage:\n%s", out)
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	c := newCLI(t)

	if _, err := c.run("", "nonsense"); err == nil {
		t.Fatal("unknown command succeeded")
	}
}

// The CLI must refuse a database serve has never prepared, rather
// than failing later with an opaque "no such table".
func TestCLIRejectsUnmigratedDatabase(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-db", filepath.Join(t.TempDir(), "fresh.db"), "player", "list"}, &out)
	if err == nil {
		t.Fatal("running against an unmigrated database succeeded")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error = %v, want it to say the database is not initialized", err)
	}
}

// The first user cannot be attributed to an admin, because none exists. The
// alternative is a database nobody can administer.
func TestUserCreateBootstrapsFirstAdmin(t *testing.T) {
	c := newCLI(t)

	out := c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "martin@example.tld", "--admin")

	if !strings.Contains(out, "Created user martin@example.tld") {
		t.Errorf("output = %q, want it to confirm the user was created", out)
	}
	if !strings.Contains(out, "Two-factor authentication is mandatory") {
		t.Error("creating an admin did not mention mandatory 2FA")
	}

	db := c.db()
	user, err := store.UserByEmail(context.Background(), db, "martin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail() failed: %v", err)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want true")
	}
	if err := auth.VerifyPassword(user.PasswordHash, "correct horse battery staple"); err != nil {
		t.Errorf("the stored password does not verify: %v", err)
	}
}

// Once an admin exists, changes must be attributed rather than guessed.
func TestMutationsRequireAnActingAdmin(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "martin@example.tld", "--admin")

	_, err := c.run("", "player", "add", "--name", "Alex")
	if err == nil {
		t.Fatal("a mutation without --as succeeded")
	}
	if !strings.Contains(err.Error(), "--as") {
		t.Errorf("error = %v, want it to name the --as flag", err)
	}
}

func TestActingAdminMustBeAnAdmin(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("correct horse battery staple\n", "--as", "admin@example.tld", "user", "create", "--email", "plain@example.tld")

	_, err := c.run("", "--as", "plain@example.tld", "player", "add", "--name", "Alex")
	if err == nil {
		t.Fatal("a non-admin was accepted as the acting admin")
	}
	if !strings.Contains(err.Error(), "not an admin") {
		t.Errorf("error = %v, want it to say the user is not an admin", err)
	}
}

func TestPasswordTooShortIsRejected(t *testing.T) {
	c := newCLI(t)

	_, err := c.run("short\n", "user", "create", "--email", "martin@example.tld", "--admin")
	if err == nil {
		t.Fatal("a short password was accepted")
	}
	if !strings.Contains(err.Error(), "at least") {
		t.Errorf("error = %v, want it to state the minimum length", err)
	}
}

func TestPlayerAddAndList(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")

	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")
	out := c.mustRun("", "--as", "admin@example.tld", "player", "list")

	if !strings.Contains(out, "martin") {
		t.Errorf("list output does not contain the player:\n%s", out)
	}
	if !strings.Contains(out, "SLUG") {
		t.Errorf("list output has no header:\n%s", out)
	}
}

// : an unset --active must not reach the database. Go's flag package
// defaults it to false, so renaming would otherwise retire the player.
func TestPlayerUpdateRenameDoesNotRetire(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	c.mustRun("", "--as", "admin@example.tld", "player", "update", "--player", "martin", "--name", "Martin S")

	player, err := store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if player.Name != "Martin S" {
		t.Errorf("Name = %q, want %q", player.Name, "Martin S")
	}
	if !player.Active {
		t.Error("renaming retired the player; an unset --active reached the column")
	}
}

func TestPlayerUpdateExplicitActiveFalseRetires(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	out := c.mustRun("", "--as", "admin@example.tld", "player", "update", "--player", "martin", "--active=false")

	player, err := store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if player.Active {
		t.Error("Active = true after --active=false")
	}
	if !strings.Contains(out, "results are kept") {
		t.Errorf("output does not say the history is kept:\n%s", out)
	}
}

func TestPlayerUpdateRequiresSomethingToChange(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	_, err := c.run("", "--as", "admin@example.tld", "player", "update", "--player", "martin")
	if err == nil {
		t.Fatal("an update with no fields succeeded")
	}
}

func TestPlayerLinkAndUnlink(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("correct horse battery staple\n", "--as", "admin@example.tld", "user", "create", "--email", "martin@example.tld")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	c.mustRun("", "--as", "admin@example.tld", "player", "link", "--player", "martin", "--user", "martin@example.tld")

	player, err := store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if player.UserID == nil {
		t.Fatal("UserID is nil after linking")
	}

	c.mustRun("", "--as", "admin@example.tld", "player", "unlink", "--player", "martin")

	player, err = store.PlayerBySlug(context.Background(), c.db(), "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug() failed: %v", err)
	}
	if player.UserID != nil {
		t.Error("UserID is set after unlinking")
	}
}

func TestPlayerUnlinkWithoutLogin(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	if _, err := c.run("", "--as", "admin@example.tld", "player", "unlink", "--player", "martin"); err == nil {
		t.Fatal("unlinking a player with no login succeeded")
	}
}

func TestUserDisableEndsSessions(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("correct horse battery staple\n", "--as", "admin@example.tld", "user", "create", "--email", "martin@example.tld")

	db := c.db()
	user, err := store.UserByEmail(context.Background(), db, "martin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail() failed: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		[]byte("session"), user.ID, "2099-01-01"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	out := c.mustRun("", "--as", "admin@example.tld", "user", "disable", "--email", "martin@example.tld")

	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Errorf("sessions remaining = %d, want 0", sessions)
	}
	if !strings.Contains(out, "sessions have ended") {
		t.Errorf("output does not mention ending sessions:\n%s", out)
	}
}

func TestSlugShowBeforeBootstrap(t *testing.T) {
	c := newCLI(t)

	// serve generates the slug, so the CLI seeing none means it has
	// never run.
	_, err := c.run("", "slug", "show")
	if err == nil {
		t.Fatal("slug show succeeded before serve bootstrapped it")
	}
	if !strings.Contains(err.Error(), "run serve once") {
		t.Errorf("error = %v, want it to point at running serve", err)
	}
}

func TestSlugShowAndRotate(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")

	db := c.db()
	original, _, err := store.EnsureShareSlug(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}

	shown := strings.TrimSpace(c.mustRun("", "slug", "show"))
	if !strings.Contains(shown, original) {
		t.Errorf("slug show = %q, want it to contain %q", shown, original)
	}
	if !strings.Contains(shown, "/share/") {
		t.Errorf("slug show = %q, want the /share/ prefix", shown)
	}

	out := c.mustRun("", "--as", "admin@example.tld", "slug", "rotate")
	if strings.Contains(out, original) {
		t.Error("rotate printed the old slug")
	}
	if !strings.Contains(out, "no longer works") {
		t.Errorf("rotate does not warn that the old link broke:\n%s", out)
	}
	if !strings.Contains(out, "reset links are unaffected") {
		t.Errorf("rotate does not clarify the scope of the change:\n%s", out)
	}
}

// APP_URL turns the path into a link someone can paste into the group chat.
func TestSlugShowUsesAppURL(t *testing.T) {
	c := newCLI(t)
	t.Setenv("APP_URL", "https://wordle.example.tld")

	db := c.db()
	if _, _, err := store.EnsureShareSlug(context.Background(), db); err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}

	out := strings.TrimSpace(c.mustRun("", "slug", "show"))
	if !strings.HasPrefix(out, "https://wordle.example.tld/share/") {
		t.Errorf("slug show = %q, want it to use APP_URL", out)
	}
}

// Who is acting is a property of the invocation, not of a verb, so --as
// lives with the global flags. Read-only verbs would otherwise advertise a
// flag they never consult.
func TestAsIsAGlobalFlag(t *testing.T) {
	c := backfillCLI(t)

	if _, err := c.run("", "--as", "admin@example.tld", "player", "list"); err != nil {
		t.Errorf("--as before the noun failed: %v", err)
	}
	if _, err := c.run("", "player", "list", "--as", "admin@example.tld"); err == nil {
		t.Error("--as parsed after the verb; it is global")
	}
	// And a read-only verb does not offer it.
	out, _ := c.run("", "player", "list", "--help")
	if strings.Contains(out, "--as") {
		t.Error("a read-only verb advertises --as, which it never consults")
	}
}

// Every flag reads the same way wherever it is named: help, errors, README.
func TestHelpUsesOneFlagConvention(t *testing.T) {
	c := backfillCLI(t)

	for _, args := range [][]string{{"--help"}, {"backfill", "--help"}, {"user", "create", "--help"}} {
		out, _ := c.run("", args...)
		for _, line := range strings.Split(out, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "--") {
				t.Errorf("%v prints a single-dash flag: %q", args, trimmed)
			}
		}
	}
}

// Asking for help is a question, not a failure: it prints to stdout and
// exits zero, at every level and in every spelling.
func TestHelpIsNotAnError(t *testing.T) {
	c := backfillCLI(t)

	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"},
		{"player", "--help"}, {"player", "-h"}, {"player", "help"},
		{"player", "add", "--help"}, {"player", "add", "-h"},
		{"backfill", "-h"}, {"slug", "-h"},
	} {
		out, err := c.run("", args...)
		if err != nil && !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v returned %v, want success", args, err)
		}
		if out == "" {
			t.Errorf("%v printed nothing", args)
		}
	}
}

// A genuine mistake still fails, or the exit code stops meaning anything.
func TestMistakesStillFail(t *testing.T) {
	c := backfillCLI(t)

	for _, args := range [][]string{
		{"player", "nonsense"},
		{"player"},
		{"nonsense"},
	} {
		if _, err := c.run("", args...); err == nil {
			t.Errorf("%v succeeded, want an error", args)
		}
	}
}

// version answers without a database, which is the point of dispatching it
// before one is opened. The moment somebody asks which build is running is
// often the moment the database is what is wrong, and the paired test above
// shows the same path being refused for every verb that needs a schema.
func TestCLIVersionNeedsNoDatabase(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-db", filepath.Join(t.TempDir(), "fresh.db"), "version"}, &out); err != nil {
		t.Fatalf("version against an unmigrated database failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != version.String() {
		t.Errorf("version printed %q, want %q", got, version.String())
	}
}

// A new verb nobody can find is a verb nobody uses.
func TestCLIUsageListsVersion(t *testing.T) {
	var out bytes.Buffer
	// "help", not "--help": the flag package intercepts the latter and
	// returns ErrHelp before the dispatch ever sees it. No -db, and that is
	// the assertion: usage must work on an install that has never run.
	if err := run([]string{"-db", filepath.Join(t.TempDir(), "never.db"), "help"}, &out); err != nil {
		t.Fatalf("help on a fresh install failed: %v", err)
	}
	if !strings.Contains(out.String(), "version") {
		t.Error("usage does not mention the version command")
	}
}
