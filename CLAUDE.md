# Wordleland

Self-hosted Wordle tracker for a group of friends. Results arrive
automatically from a Signal group; manual entry and admin correction also
supported.

**Stack:** Go · SQLite (`modernc.org/sqlite`, pure Go) · `net/http` ·
`html/template`, server-rendered · Docker.

Standard library first. A dependency needs a reason — the point of this stack
is a small footprint and a small attack surface. No npm, no SPA framework, no
client-side rendering by default. Charts are server-generated SVG.

`internal/web/static/app.js` is the one exception, and a narrow one: vanilla,
no build step, no dependency, purely a progressive enhancement (see its
header comment). Whether and how to use JavaScript more broadly is an open
question, not yet decided — do not treat this file as having settled it.

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
- The visual design is produced in Claude Design and is **not in this
  repository** — it is being redone. The previous export, and a brief
  recording where the build departs from it on purpose, are kept on the
  `design/export` branch.

When an export is imported again, it is a **visual reference only** — layout,
spacing, type, colour, chart form — and the code wins wherever they conflict.
It carries hardcoded sample data, a fixed roster and placeholder copy, none
of which are requirements. Never port a literal player list, a literal roster
count, or gendered pronouns from it. It is also a JavaScript artifact; it
describes what the pages should look like, not how they are built.

## Personal data

Never commit personal data about anyone other than the repo owner. That
includes real names, Signal display names, account UUIDs, phone numbers,
email addresses, and real scores belonging to identifiable people. Use
"Martin" or synthetic names in examples, docs, fixtures and test data.

This applies to imported material too: the Claude Design export was built
on the real roster and contains other people's names and scores. Replace
them with synthetic data before committing it, or don't commit the file.

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
