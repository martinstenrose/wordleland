# Wordleland repository instructions

These shared instructions apply to all coding agents and human contributors
working in this repository.

Self-hosted Wordle tracker for a group of friends. Results arrive
automatically from a Signal group; manual entry and admin correction also
supported.

**Stack:** Go · SQLite (`modernc.org/sqlite`, pure Go) · `net/http` ·
`html/template`, server-rendered · Docker.

Standard library first. A dependency needs a reason — the point of this stack
is a small footprint and a small attack surface. No npm, no SPA framework, no
client-side rendering by default. Charts are server-generated SVG.

`internal/web/static/app.js` carries the JavaScript this app ships, and the
rule for what belongs there is a narrow one, decided deliberately rather
than left to whether a given feature "adds value": **JS is for what cannot
exist without it** — a keyboard shortcut, an overlay with no page behind it
— not for making an already-working feature nicer. Concretely:

- Everything JS adds must degrade to a working, server-rendered path. A
  feature is the server-rendered route first; script only adds a shortcut
  or an inline affordance on top of it. The search box (`GET /search`,
  `internal/web/search.go`) is the model: the route works with zero
  script, and the `⌘K` overlay is `app.js` fetching that same route's
  markup, not a second implementation of search.
- Vanilla, no build step, no npm dependency — unchanged. Needing more than
  that is a sign to have this conversation again, not a reason to reach
  for a bundler or a framework quietly.
- New client behaviour gets a header comment stating what it does and
  what still works with it absent, disabled, or failing to load — the
  convention `app.js`'s existing popup-positioning code already follows.
- Where a Go test can pin the server-rendered fallback, it does; where the
  behaviour is JS-only with nothing to fall back to (arrow-key navigation
  inside an open overlay, say), that gap is stated rather than papered
  over with a test that doesn't actually exercise a browser.

This was an open question for a while — do not read an old comment or PR
elsewhere as having settled it any other way than what's written here.

**Layout:** one Go module, one binary.

- `cmd/wordleland` — `serve` runs the server and, when Signal is configured,
  the bridge. The same binary carries the admin verbs (users, players,
  corrections, backfill).
- `internal/web` · `internal/store` · `internal/ingest` · `internal/bridge` ·
  `internal/wordle` · `internal/stats` · `internal/auth` · `internal/config`

`internal/ingest` holds the rules for filing a result, because the HTTP
endpoint and the Signal bridge both need them and a second copy would drift.

Two compose services: `app` and `signal-cli-rest-api` (bbernhard image,
off-the-shelf). Self-hosted with Docker Compose: no platform-as-a-service,
no managed database, and nothing in the deploy that needs more than Docker
and an `.env` file.

Auth is hand-rolled (argon2id, server-side sessions, TOTP via `pquerna/otp`).
This is deliberate; do not introduce an auth framework. The details that are
ours to get right — enrolment order, secret encryption, replay protection,
rate limiting, the two-step login — are commented where they happen.

## Sources of truth

- **The code is authoritative on what.** The schema, the routes and the CLI
  describe themselves, and the comments carry the local reasons. There is no
  separate specification to keep in step, deliberately — there was one, and
  it drifted.
- `docs/decisions.md` holds what the code cannot: findings from data that is
  not in this repository, arguments that span several packages, and the
  things deliberately not built. Read it before changing how scores are
  counted, how identities resolve, or anything in auth.
- `README.md` is for whoever runs this, the owner included: what it does,
  how to run it, what every variable means, and what goes wrong. Written for
  a stranger, because the owner is one too a year later. Read it before
  touching configuration, the deploy, or a CLI verb — those are what it
  documents, and a change there that leaves it stale is the same mistake as
  leaving `docs/decisions.md` stale.
- This file is for whoever works on the code, human or agent: how the
  project is built, why it is built that way, and the standing constraints.
- Neither is a place for who the owner is or what else they run. A rule that
  reads as being about one person belongs in neither.

## Personal data

Never commit personal data about anyone other than the repo owner. That
includes real names, Signal display names, account UUIDs, phone numbers,
email addresses, and real scores belonging to identifiable people. Use
"Martin" or synthetic names in examples, docs, fixtures and test data.

**Nor does the repository describe where or how any particular copy of it is
run.** No hostnames, no domains, no orchestration or hosting products, no
network layout. That is somebody's private infrastructure, and it is of no
use to a reader anyway: what belongs here is the *constraint* a decision was
made against — self-hosted, no managed database, mail optional. "A
deployment with no mail server is supported" is the useful half; naming the
machine is the half that should not be public.

## Contributing

Branch naming, commit message conventions, and "every commit builds on its
own" are in README.md's Contributing section — that file is for whoever
runs and works with the project from outside it, which a contributor is
before anything else.

Every commit written with an agent includes a final `Co-Authored-By` trailer
naming the model that did the work, using its provider's noreply address.
Follow the style already established in the history:

```
Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Co-Authored-By: Codex GPT-5.6 Sol <noreply@openai.com>
```

Use the identity and full model version of the agent that actually wrote the
commit, including the minor version when it has one. Do not omit the trailer,
replace it with prose in the pull request, or copy an example's identity when
a different model did the work.

## Before a change is done

**A fix gets a test that fails without it.** Then check that it does: remove
the fix, watch the test go red, put it back. A test written from the fixed
code often passes either way, which is worse than no test — it makes the
next person confident about something nobody checked. The same goes for a
guard against a case that cannot currently happen: if the assertion can
never fire, it is decoration.

**Say what was not verified.** A summary that lists checks implies they all
ran. If something was skipped, could not be exercised here, or was only
covered indirectly, say so in that many words.

**Documentation follows behaviour, but only where it is not already
written.**

- `docs/decisions.md` — when the *reason* changes and the code cannot carry
  it: a scoring rule, how identities resolve, anything in auth, and anything
  a reader would otherwise have to reverse-engineer from data not in this
  repository. Also when something moves out of "Known problems", so the
  file does not keep warning about a thing that is fixed.
- `README.md` — when running or operating it changes: a variable, a volume,
  a command, a workflow.
- A comment, when the reason is local to the code it explains.

Do not restate what the code already says. A comment or a doc line that
repeats the signature above it is a line that will go stale and mislead.

**One name per thing.** The same concept under two names in the code, the UI
and the schema is how "dashboard", "board" and "leaderboard" ended up
meaning one page. If a rename is right, finish it — comments, test names,
user-facing copy and commit scopes included — rather than leaving the old
word in the places nothing compiles against.

**Checks before pushing:** `gofmt -l ./cmd ./internal`, `go vet ./...`,
`staticcheck ./...`, `go test -race ./...`. CI also runs `govulncheck` and
CodeQL, weekly as well as per pull request. A deliberate lint exception
carries a `//lint:ignore` naming the reason.

The `go` directive in `go.mod` is a **floor**, and CI installs exactly it.
Dependabot does not raise the patch version, so it goes stale silently and
takes the whole standard library's advisories with it; `govulncheck` is what
notices.
