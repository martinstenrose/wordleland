// Command wordleland is the admin CLI. It runs against the database directly
// rather than through the HTTP API, so it works before any login exists and is
// not subject to the ingest token precedence rule.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	// Puzzle dates are derived in time.Local, and the distroless base image
	// ships no timezone files.
	_ "time/tzdata"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/version"
)

// env is everything a subcommand needs.
type env struct {
	ctx    context.Context
	db     *sql.DB
	dbPath string
	// adminEmail identifies who is acting, for entered_by attribution and the
	// audit log. The CLI has no session, so it must be told.
	adminEmail string
	out        io.Writer
}

func main() {
	err := run(os.Args[1:], os.Stdout)
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		// Asking for help is not a failure. The flag package returns this
		// for --help and -h alike, and reporting it as an error meant every
		// help screen ended in "error: flag: help requested" and exited 1.
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = `wordleland — Wordleland admin CLI

Usage:
  wordleland [global flags] <noun> <verb> [flags]

Global flags come before the noun; a verb's own flags come after it:

  wordleland --as you@example.tld player update --player martin --active=false

Commands:
  serve     run the server, and the Signal bridge when configured
  version   print the running build

Nouns:
  user      create, reset-password, reset-2fa, disable, enable
  player    add, update, link, unlink, list
  identity  pending, claim, discard, add
  results   set, unset
  token     create, list, revoke
  backfill  import history from the spreadsheet
  slug      show, rotate

Global flags:
  --db <path>      database path (default %s)
  --as <address>   the admin performing the change; defaults to $ADMIN_EMAIL

Every verb that writes needs --as, so the change is attributed to somebody.

Run "wordleland <noun>" to see that noun's verbs.
`

func run(args []string, out io.Writer) error {
	global := flag.NewFlagSet("wordleland", flag.ContinueOnError)
	global.SetOutput(out)
	dbPath := global.String("db", "", "path to the SQLite database")
	asEmail := global.String("as", "", "`address` of the admin performing the change")
	global.Usage = func() { fmt.Fprintf(out, usage, config.DefaultDBPath) }

	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return errors.New("no command given")
	}

	if *dbPath == "" {
		*dbPath = config.DefaultDBPath
	}
	if *asEmail == "" {
		*asEmail = os.Getenv("ADMIN_EMAIL")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// serve is dispatched before the database is opened, because it is the
	// verb that migrates: everything below insists on a schema it has
	// already applied.
	if rest[0] == "serve" {
		return runServe(ctx, rest[1:], *dbPath, out)
	}

	// Dispatched up here with serve, and for the same shape of reason: it
	// needs no database, and the moment somebody asks which build is
	// running is often the moment the database is the thing that is wrong.
	// A version verb that could not answer without a healthy schema would
	// be unavailable exactly when it is wanted.
	if rest[0] == "version" {
		fmt.Fprintln(out, version.String())
		return nil
	}

	// OpenMigrated, not Open: serve owns migrations, so a database it has
	// never touched is an operator mistake worth naming rather than a schema
	// to create from a second process.
	db, err := store.OpenMigrated(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	e := &env{ctx: ctx, db: db, dbPath: *dbPath, adminEmail: *asEmail, out: out}

	noun, verbArgs := rest[0], rest[1:]
	switch noun {
	case "user":
		return runUser(e, verbArgs)
	case "player":
		return runPlayer(e, verbArgs)
	case "identity":
		return runIdentity(e, verbArgs)
	case "results":
		return runResults(e, verbArgs)
	case "token":
		return runToken(e, verbArgs)
	case "backfill":
		return runBackfill(e, verbArgs)
	case "slug":
		return runSlug(e, verbArgs)
	case "help", "-h", "--help":
		global.Usage()
		return nil
	default:
		global.Usage()
		return fmt.Errorf("unknown command %q", noun)
	}
}

// actor resolves who is performing a change.
//
// Every mutation is attributed, and the CLI has no session to infer it
// from. Requiring it explicitly is deliberate: silently attributing changes to
// "the first admin" would make the audit log a guess.
func (e *env) actor() (store.Actor, error) {
	if e.adminEmail == "" {
		return store.Actor{}, errors.New(
			"no acting admin: pass --as <address> before the noun, " +
				"as in \"wordleland --as you@example.tld player add --name Martin\", " +
				"or set ADMIN_EMAIL")
	}
	user, err := store.UserByEmail(e.ctx, e.db, e.adminEmail)
	if errors.Is(err, store.ErrUserNotFound) {
		return store.Actor{}, fmt.Errorf("no user with email %s", e.adminEmail)
	}
	if err != nil {
		return store.Actor{}, err
	}
	if !user.IsAdmin {
		return store.Actor{}, fmt.Errorf("%s is not an admin", user.Email)
	}
	return store.AdminActor(user.ID), nil
}

// subcommand describes one verb.
type subcommand struct {
	name    string
	summary string
	run     func(*env, []string) error
}

// dispatch runs the named verb, or prints the available ones.
func dispatch(e *env, noun string, cmds []subcommand, args []string) error {
	printVerbs := func() {
		fmt.Fprintf(e.out, "Usage: wordleland %s <verb> [flags]\n\nVerbs:\n", noun)
		for _, c := range cmds {
			fmt.Fprintf(e.out, "  %-16s %s\n", c.name, c.summary)
		}
	}

	if len(args) == 0 {
		printVerbs()
		return fmt.Errorf("no %s verb given", noun)
	}
	// Asking a noun what it can do is a question, not a mistake: without
	// this it fell through to "unknown verb" and exited 1.
	switch args[0] {
	case "help", "-h", "--help":
		printVerbs()
		return flag.ErrHelp
	}
	for _, c := range cmds {
		if c.name == args[0] {
			return c.run(e, args[1:])
		}
	}
	printVerbs()
	return fmt.Errorf("unknown %s verb %q", noun, args[0])
}

// flagSet builds a flag set that reports errors through the command's output.
//
// It carries no --as: who is acting is a property of the invocation rather
// than of any one verb, the same way the database path is, so it lives with
// the global flags. Read-only verbs would otherwise advertise a flag they
// never consult.
func flagSet(e *env, name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.out)
	fs.Usage = func() { printFlags(e.out, name, fs) }
	return fs
}

// printFlags lists a verb's flags with the same double dash the errors, the
// README and the global help use.
//
// It exists because flag.PrintDefaults prints a single dash and cannot be
// told otherwise. Both forms parse identically, so this is about the help
// agreeing with everything else that names a flag rather than about what
// works.
func printFlags(out io.Writer, name string, fs *flag.FlagSet) {
	var flags int
	fs.VisitAll(func(*flag.Flag) { flags++ })
	if flags == 0 {
		// A read-only verb. Printing an empty "Flags:" heading invites the
		// reader to look for something that is not there.
		fmt.Fprintf(out, "Usage: wordleland %s\n", name)
		return
	}

	fmt.Fprintf(out, "Usage: wordleland %s [flags]\n\nFlags:\n", name)
	fs.VisitAll(func(f *flag.Flag) {
		placeholder, usage := flag.UnquoteUsage(f)
		switch placeholder {
		case "value", "string":
			// What flag reports for a Func and a String: the type, which
			// tells a reader nothing about what to type. A backquoted word
			// in the usage text names it properly; otherwise fall back to
			// the flag's own name.
			placeholder = f.Name
		case "":
			// A bool. It takes nothing.
		}
		left := "--" + f.Name
		if placeholder != "" {
			left += " <" + placeholder + ">"
		}
		fmt.Fprintf(out, "  %-24s %s\n", left, usage)
	})
}

// requireFlag returns an error naming the missing flag.
func requireFlag(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--%s is required", name)
	}
	return nil
}
