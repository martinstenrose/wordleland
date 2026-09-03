# Wordleland

[![ci](https://github.com/martinstenrose/wordleland/actions/workflows/ci.yml/badge.svg)](https://github.com/martinstenrose/wordleland/actions/workflows/ci.yml)
[![security](https://github.com/martinstenrose/wordleland/actions/workflows/security.yml/badge.svg)](https://github.com/martinstenrose/wordleland/actions/workflows/security.yml)
[![release](https://github.com/martinstenrose/wordleland/actions/workflows/release.yml/badge.svg)](https://github.com/martinstenrose/wordleland/actions/workflows/release.yml)

Self-hosted Wordle tracker for a group of friends. Results arrive
automatically from a Signal group; manual entry and admin correction are
also supported.

`docs/decisions.md` explains why things are the way they are and wins wherever this file and it
disagree. This file covers running the thing.

## What runs

Two containers:

| Service | What it does |
|---|---|
| `app` | Everything of ours. Owns the database, serves the board, exposes `/api/ingest`, and runs the Signal bridge when one is configured. The admin CLI is the same binary. |
| `signal-cli-rest-api` | Off-the-shelf. Holds the Signal connection as a linked device. |

Only `app` is reachable from outside, through Caddy. The Signal container is
deliberately not published: it holds the linked device's credentials, and
nothing but `app` needs to reach it.

**The Signal bridge is optional.** Leave `SIGNAL_ACCOUNT` and
`SIGNAL_GROUP_ID` unset and the app serves the board without it, which is
how a fresh install imports its history before Signal is linked. Set them
together — one without the other is refused, because it would start, watch
nothing, and look exactly like a deployment that works.

## Configuration

Copy `.env.example` to `.env` and fill it in. `.env` is gitignored.

`TZ` applies to both services and is set once in `.env`, defaulting to
`Europe/Stockholm` when unset. It is read by the Go runtime rather than by
the app, and it decides which day a puzzle number belongs to — so getting it
wrong shifts every date by a few hours rather than failing outright.

### App

| Variable | Required | Meaning |
|---|---|---|
| `APP_URL` | when SMTP is set | Public origin, no path. Emailed links are built from this and never from the request `Host` header — otherwise someone could request a reset for your account with a forged header and have the link point at their own server. |
| `TOTP_KEY` | yes | 32 bytes, base64: `head -c 32 /dev/urandom \| base64`. Encrypts every TOTP secret at rest. Validated at boot. |
| `TRUSTED_PROXIES` | in practice | CIDRs of the reverse proxy. See the warning below. |
| `SMTP_*` | no | Absent means password reset by email is unavailable and the rest of the app runs normally. |
| `PENDING_RETENTION` | no | How long unclaimed results are held. Empty means indefinitely. |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | no | Creates the first administrator on a fresh database. Both together; at least 12 characters. Ignored once any user exists, so they can be removed after the first boot. |
| `DEMO_MODE` | no | Arms the `demo` CLI verb, which generates and deletes players. See [Running a staging instance](#running-a-staging-instance). Must never be `true` on the instance the group actually uses. |

The database path and listen port are not configurable: always
`/data/db.sqlite` and `:8080`. A volume decides where the file really
lives, and the port is invisible behind the proxy. The binary accepts
`--db <path>` for running outside a container.

For SMTP submission, use a relay endpoint that advertises STARTTLS (normally
port 587). The Go SMTP client upgrades automatically when STARTTLS is offered;
if a received message reports an unencrypted last hop, the configured relay
did not offer it. Transactional account mail should also have open and click
tracking disabled at the relay: injected tracking pixels add hidden content
and an extra URL that commonly attract spam-filter rules.

### Signal bridge

| Variable | Meaning |
|---|---|
| `SIGNAL_ACCOUNT` | The number the bot receives on: its own if registered, the operator's if linked. E.164, leading `+`, exactly as `/v1/accounts` reports it. **Quote it in YAML** — unquoted, `+46…` is parsed as an integer and loses the `+`, which produces a bridge that connects and receives nothing. |
| `SIGNAL_GROUP_ID` | See below — this one is easy to get wrong. |

`SIGNAL_API_URL` is not configured. It defaults to
`http://signal-cli-rest-api:8080` — a service name from `compose.yml` joined
to the port that image always exposes, so it cannot change without editing
that file anyway. Set it as an environment variable to override it, which is
what running outside compose needs.

There is no ingest token to provision. `/api/ingest` and its tokens remain
for curl and any future bridge, but the Signal bridge runs inside the app
and does not need a credential to talk to itself.

**`SIGNAL_GROUP_ID` is the bare base64 `internal_id`** from `GET /v1/groups`,
not the `group.<base64>` value the same endpoint reports as `id`. Only the
first matches what arrives on a message. The bridge refuses the prefixed
form at boot, because configuring it would otherwise produce a bot that
connects, reports itself healthy, and matches nothing for ever.

### Set `TRUSTED_PROXIES`

Behind a reverse proxy every request arrives from the proxy's address. With
this empty, the login rate limiter treats the whole internet as one client:
ten failed logins from anyone locks out login for everyone for fifteen
minutes, and the per-address protection is gone. `172.16.0.0/12` covers the
default Docker bridge range.

`X-Forwarded-For` is believed only from an address in this list, and the
rightmost entry that is not itself trusted is taken — a client can prepend
anything it likes to that header, but cannot forge what a trusted proxy
appends.

Several ranges are comma-separated, and IPv4 and IPv6 mix freely — a proxy
on a dual-stack network needs both listed, or requests arriving over the one
you left out are treated as coming straight from the internet:

```
TRUSTED_PROXIES=172.18.0.0/16,fd00:1234:5678:9abc::/64
```

`docker network inspect <network> --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}'`
prints the ranges to use.

**Do not set a catch-all.** There is no wildcard syntax, and the equivalent
CIDR is worse than leaving this empty rather than easier: trusting every
address means no entry in the header is ever untrusted, so the walk finds
nothing to stop at and falls back to the address the connection came from —
the proxy. Every client in the world then shares one rate-limit key, which
is exactly the failure this variable exists to prevent. Name the range the
proxy is actually on.

### Rotate `TOTP_KEY` before anyone enrols

That key encrypts every TOTP secret at rest, so rotating it makes all of
them unrecoverable and everyone must be reset with `user reset-2fa` and
enrol again. Before the first enrolment it costs nothing; afterwards it
costs a round of re-enrolment for the whole group. It belongs in your
backups.

## Behind a reverse proxy

The app publishes no ports: it listens on `:8080` inside its own
container and expects something in front of it to terminate TLS. How that
is wired is deployment-specific and deliberately not in this repo.

With `caddy-docker-proxy`, that means adding labels and a shared network to
the `app` service locally:

```yaml
    networks:
      - default
      - caddy
    labels:
      caddy: wordle.example.tld
      caddy.reverse_proxy: "{{ upstreams 8080 }}"
```

```yaml
networks:
  caddy:
    external: true
```

**Keep `default` in that list.** Declaring `networks:` on a service
*replaces* the default rather than adding to it, so an `app` listed only
under `caddy` is no longer on the network `signal-cli-rest-api` is on. Both
containers start, both look healthy, and nothing is ever received:

```
dial tcp: lookup signal-cli-rest-api on 127.0.0.11:53: server misbehaving
```

That message is the Docker resolver saying the name is not on any network
this container is attached to, rather than anything being wrong with DNS.

The network is called `default` inside the compose file. `wordleland_default`
is the name Docker gives it externally, and using that here fails with
`refers to undefined network`.

Whatever proxy is used, set `TRUSTED_PROXIES` to match it.

## Images

Published to GHCR by `.github/workflows/release.yml`:

```
ghcr.io/martinstenrose/wordleland
```

It is built for `linux/amd64` and `linux/arm64`. The Go build
cross-compiles from the runner's architecture, so the second platform costs
a compile rather than an emulated build.

Tags published, by trigger:

- **A version tag** (`v1.2.3`) — publishes `1.2.3`, the major.minor `1.2`,
  the full commit SHA, and moves `latest`. The `v` is a git tag convention
  and is dropped from the image tag, which is what every registry does and
  what `docker pull` reads naturally.
- **A push to `main`** — publishes `testing` and the full commit SHA, so
  there's always an image for the tip of the branch without cutting a
  release. Only the latest push in a burst is actually built: pushing to
  main cancels any build already running for main, so several merges
  landing within a minute produce one build of the final state, not one
  each.
- **The Run workflow button** on the repository's Actions tab, against any
  branch — publishes that branch's name and the full commit SHA. Running it
  against `main` publishes `testing`, same as a normal push.

Only a version tag ever moves `latest`: deploying `latest` should never pick
up whatever was last merged or pushed by hand.

`go vet` and `go test` run first, and a failure stops the publish.

Compose follows `latest`. Pin a version at deploy time by editing the image
tag.

**Image tags are not immutable.** This Compose file deliberately uses tags
for both services so ordinary deployments can receive updates, but a registry
owner can move or replace any tag — including a version tag. A compromised
publisher or registry could therefore make a later `docker compose pull`
download different content under a familiar name. The repository's update
scanning cannot protect a deployment from that supply-chain risk.

The `wordleland` image is built and published by this repository.
`signal-cli-rest-api` is different: it is an off-the-shelf third-party image,
not source this project builds or controls. Its provenance, release process
and tag integrity belong to that upstream publisher, so an operator must
assess and pin it separately when the deployment requires that guarantee.

An operator who requires reproducible, immutable deployments should override
each image with a registry digest, in the form
`image:tag@sha256:<digest>`. That binds the deployment to the exact published
manifest rather than trusting the tag. Digest pins do not update themselves:
the operator must review and replace them to receive security fixes, for both
Wordleland and `signal-cli-rest-api`. This is a deployment policy rather than
an application default, so the provided Compose file remains tag-based.

```sh
docker compose pull && docker compose up -d
```

If the host has the repository checked out, `compose.override.yml` is picked
up there too and adds build contexts to both services. `docker compose -f
compose.yml up -d` ignores the override and uses only what was pulled.

Publishing needs no secret: the workflow authenticates with the built-in
`GITHUB_TOKEN`. The packages are private by default — make them public, or
`docker login ghcr.io` on the host with a token that has `read:packages`,
before pulling.

## First run

```sh
cp .env.example .env      # then fill it in
docker compose pull       # or `docker compose build` to build locally
docker compose up -d
```

With `ADMIN_EMAIL` and `ADMIN_PASSWORD` set, the first administrator is
created on startup and the log says so. Sign in; two-factor enrolment is
mandatory for admins and happens immediately.

Those two variables act only when no user exists at all, so leaving them in
`.env` is harmless: they cannot change a password that has since been
changed, and cannot bring back an account that was deliberately removed.

## Connecting Signal

Two ways to give the bot a Signal identity. **Registering its own number is
the better one** and is what this section leads with: the bot owns the
account, nothing is tied to a personal phone, and there is no QR code in the
loop at all.

Everything below talks to signal-cli-rest-api, which publishes no ports
normally. Reach it over the Docker network:

```sh
sig() { docker run --rm --network wordleland_default curlimages/curl -s "$@"; }
```

### Registering a new number

The number needs to receive an SMS or a voice call once. Anything works — a
spare SIM, a data-only eSIM, a VoIP number that accepts SMS.

```sh
sig -X POST -H 'Content-Type: application/json' \
  http://signal-cli-rest-api:8080/v1/register/+46700000000
```

Signal usually demands a captcha:

```json
{"error":"Captcha required for verification (null)\n"}
```

If so, open <https://signalcaptchas.org/registration/generate.html>, solve
it, and take the token out of the developer console — the line reading
`Prevented navigation to "signalcaptcha://{token}" due to an unknown
protocol`. Copy only the token, without the `signalcaptcha://` prefix, then:

```sh
sig -X POST -H 'Content-Type: application/json' \
  --data '{"captcha":"signal-hcaptcha-short.xxxx.registration.yyyy"}' \
  http://signal-cli-rest-api:8080/v1/register/+46700000000
```

Add `"use_voice": true` to the body for a phone call instead of an SMS.

Verify with the code that arrives:

```sh
sig -X POST -H 'Content-Type: application/json' \
  http://signal-cli-rest-api:8080/v1/register/+46700000000/verify/123456
```

If the number has a registration PIN, send it as `{"pin":"..."}` in that
body.

Confirm it took — this lists the number once registration has worked, and is
an empty array before:

```sh
sig http://signal-cli-rest-api:8080/v1/accounts
```

Then add the bot to the Wordle group from your own phone, and find the group
id:

```sh
sig 'http://signal-cli-rest-api:8080/v1/groups/+46700000000'
```

Take **`internal_id`**, not `id`, into `SIGNAL_GROUP_ID`, and the number into
`SIGNAL_ACCOUNT`.

### Linking as a device on an existing account

The alternative: the bot rides along on a personal Signal account as a linked
device. It works, but it ties the bot to a personal number, and the link step
needs a QR code scanned within about a minute.

```sh
sig 'http://signal-cli-rest-api:8080/v1/qrcodelink/raw?device_name=wordleland' \
  | qrencode -t ANSIUTF8i -m 2
```

Then scan it from Signal → Settings → Linked devices → Link new device.

**Use the inverted output — `ANSIUTF8i`, not `ANSIUTF8`.** On a dark
terminal the non-inverted form renders light modules on a dark ground, which
phone cameras generally refuse to read. `UTF8i` is the same thing without
colour codes if the terminal mangles those.

Note that the PNG endpoint cannot feed this: `qrencode` *generates* codes
from text and does not read images, so piping `/v1/qrcodelink` into it does
nothing useful. `/v1/qrcodelink/raw` returns the `sgnl://linkdevice?...` URI
as text, which is what `qrencode` wants. It needs signal-cli-rest-api 0.100
or newer; 0.94 returns 404.

If Signal calls the code invalid, the app read the QR and rejected its
**contents** — the URI had expired, or was truncated. Each request
invalidates the previous one, so re-run the pipeline and scan promptly rather
than adjusting how it is displayed.

## Editing players

A signed-in admin has `/admin/players`: the roster with each player's slug,
linked login, game count and membership, and an edit form behind each name
covering the display name, the slug, the linked login and whether they are
still in the group.

It writes through the same code the CLI does, so every change lands in the
audit log against the admin who made it. There is no delete — retirement is
clearing "still in the group", which keeps the history (§4). Everything else
in the CLI below is still CLI-only.

Changing a slug changes that player's URL, and the share link is a
capability URL people may have bookmarked. Renaming is cheap; re-slugging is
not free.

## The admin CLI

It is the same binary as the server and is invoked by absolute path,
because that image is distroless: no shell, so no `PATH` to resolve a bare
name against.

```sh
docker compose exec app /wordleland <noun> <verb> [flags]
```

`docker compose exec app bash` will not work, and neither will `ls` —
there is nothing in the image but the binary.

Every command that writes needs an acting admin, either `--as <address>`
before the noun or
`ADMIN_EMAIL` in the environment. The first user is the exception: nothing
exists yet that could authorise it.

Two commands need no database and no acting admin, so they answer even when
the schema has not been created or is the thing that is broken:

```sh
docker compose exec app /wordleland version
docker compose exec app /wordleland help
```

```sh
# An ingest token, for a curl client or another bridge. Shown once.
# The Signal bridge does not need one: it runs inside the app.
docker compose exec app /wordleland token create --label scripts

# The read-only share link.
docker compose exec app /wordleland slug show
docker compose exec app /wordleland slug rotate

# Players.
docker compose exec app /wordleland player add --name "Martin"
docker compose exec app /wordleland player list
docker compose exec app /wordleland player update --player martin --active=false

# Senders whose results are waiting, and claiming them.
docker compose exec app /wordleland identity pending
docker compose exec app /wordleland identity claim \
  --player martin --source signal --external-id <uuid> --dry-run

# Corrections. A hand-entered value wins over anything the Signal bridge sends.
docker compose exec app /wordleland results set --player martin --puzzle 1893 --guesses 4 --hard-mode
docker compose exec app /wordleland results unset --player martin --puzzle 1893
```

The share link is a read-only capability: anyone who knows its URL can view
the board without signing in, but cannot change anything. Its path is visible
to the application and reverse proxy and may be retained in either service's
access logs or in a log aggregator. Treat access to those logs as
administrative access, and rotate the share slug if it is disclosed outside
that trusted boundary.

## Onboarding a player

The bridge does not know the roster, and identities are keyed on the
Signal account UUID rather than the display name, which anyone can change.
So a new player is claimed rather than guessed:

1. They post a result. It is held, and nothing reaches the board yet.
2. `identity pending` lists the sender with a display hint and how many
   results are waiting.
3. `identity claim` maps them to a player and replays everything held.

Until claimed, their results wait rather than being lost. Claiming with
`--dry-run` first shows what would be replayed.

## Importing history

```sh
docker compose cp results.csv app:/tmp/results.csv
docker compose exec app /wordleland --as you@example.tld backfill \
  --file /tmp/results.csv --dry-run
```

The results file is `Date,Wordle` followed by one column per player. Cells
are `1`–`6`, `X`, either with a trailing `*` for hard mode, or empty for did
not play.

With no `--mapping`, each column header is read as a player slug and every
player is imported as active. A header that is not a slug is reported by
name, so the choice is to rename it or pass a mapping.

The mapping file is for the cases that need it: headers that are display
names rather than slugs, or a roster where somebody should be imported as
having left. It is `column_header,player_slug,active`, and supplying one
makes the two files agree in both directions — an unmapped column and a
mapping row matching no column both abort the run.

```sh
docker compose cp mapping.csv app:/tmp/mapping.csv
docker compose exec app /wordleland --as you@example.tld backfill \
  --file /tmp/results.csv --mapping /tmp/mapping.csv --dry-run
```

Backfill is an import rather than a sync: with a mapping it applies the
`active` column on every run, so re-running it after the roster has moved
on will resurrect anyone since retired.

## Running a staging instance

A staging instance has no Signal group to post to it, so its board is empty
until something fills it. `demo seed` and `demo tick` generate synthetic
players and history for exactly that; `demo clear` removes them again. All
three are gated on `DEMO_MODE=true` and refuse to run otherwise, and `serve`
logs a warning at boot whenever it finds the flag set — it must never be set
on the instance the group actually uses.

Give staging its own compose project name and its own `.env`, separate from
the real deployment's, so the two never share a volume:

```sh
docker compose -p wordleland-staging up -d
```

Its `.env` needs:

- `DEMO_MODE=true`
- its own `TOTP_KEY`, generated the same way as the real one and never
  reused across instances
- its own `APP_URL`, since emailed links are built from it
- `SIGNAL_ACCOUNT` and `SIGNAL_GROUP_ID` left unset, so no bridge starts

`signal-cli-rest-api` still starts alongside `app` — `compose.yml` does not
offer a way to leave it out — but with no account linked to it, it sits idle
and does nothing.

Keep `ADMIN_EMAIL` / `ADMIN_PASSWORD` set, the same as on a real deployment:
they only bootstrap the first administrator and are ignored once one exists,
so leaving them in place costs nothing and means a rebuilt staging instance
can always log back in. If `SMTP_*` is configured at all, point it at
something that will not deliver to a real address — a catch-all or a
disabled sender — since the synthetic roster is not a mailing list anyone
consented to join.

```sh
docker compose exec app /wordleland --as you@example.tld demo seed \
  --players 12 --days 200
```

`--seed <N>` makes a run reproducible; without it, seeding is left to vary.
Run it once against a fresh database. Running it again on top of an existing
seed is not a reset — it creates a second roster alongside the first, and
`player add`'s slug collisions are the only thing that would stop it. Use
`demo clear` first if the intent was to start over.

```sh
docker compose exec app /wordleland --as you@example.tld demo tick
```

Run this once a day, from cron on the host or an equivalent scheduler, to
keep the board moving after the initial seed. It is safe to run more than
once a day: a player who already has today's result is left alone rather
than replayed with a new one, and a retired player is never brought back.

```sh
docker compose exec app /wordleland --as you@example.tld demo clear --apply
```

Without `--apply` it only reports what it would delete. This removes every
player and their results, identities and held pending senders — everything
`demo seed` could have produced — but never the administrator account or
its 2FA enrolment, and never the audit log. It is a narrower operation than
resetting the deployment: `docker compose down -v` also drops the database
file itself, along with the admin account and the share link, so the next
boot bootstraps from `ADMIN_EMAIL` / `ADMIN_PASSWORD` again from nothing.
`demo clear` is for generating a new synthetic roster without redoing that
setup.

## Monitoring

`GET /healthz` on `:8080` is a **liveness** probe and only that. It answers
one question: would restarting help?

It fails when the database is unreachable, or when a configured Signal
bridge has stopped — the supervisor gives up after repeated panics, which is
a bug a restart genuinely clears.

It stays green when the bridge is merely disconnected and retrying. Bouncing
the app cannot reach signal-cli, and would only interrupt the backoff that is
already fixing it. Since the services merged, failing this probe takes the
board down too, so it is reserved for faults a restart addresses.

Everything else — when a result last arrived, how current the board is, what
is held for unclaimed senders, whether the bridge is connected, and which
build is running — is on **Admin → Diagnostics**, and a warning line follows you around the admin area
when something there needs attention. That page leads with freshness rather
than connection state on purpose: a bridge pointed at the wrong group is
connected, answering, and delivering nothing, and a connection indicator is
green throughout.

The version there is stamped into the binary at image build time, so it
describes the container you are actually looking at rather than the tag you
believe you deployed. A published image reports its release or the rolling
`testing` tag, with the commit beside it; a locally built one reports `dev`,
since the build context excludes `.git` and has nothing to read a commit
from. `wordleland version` prints the same string without opening a browser.

## Development

```sh
go build ./...
go test ./...
```

Running the server directly, without Docker, needs the same environment
`compose.yml` would otherwise supply. Global flags (`--db`, `--as`) come
before the noun — unlike a Docker `exec`, where they follow `/wordleland`
but still precede the noun the same way:

```sh
TOTP_KEY=$(head -c 32 /dev/urandom | base64) \
APP_URL=http://localhost:8080 \
ADMIN_EMAIL=you@example.tld \
ADMIN_PASSWORD=<12+ characters> \
  go run ./cmd/wordleland --db ./db.sqlite serve
```

`APP_URL` matters here even though nothing sends mail: unset, cookies
default to `Secure`, and some browsers won't send a `Secure` cookie back
over plain `http://localhost`, which fails login with what looks like an
expired-session error rather than anything about cookies. Setting it to the
origin you're actually browsing from fixes that.

Two-factor is mandatory for the admin account, deliberately with no
bypass. Generate codes from a terminal instead of a phone, against the
secret the enrolment screen shows next to its QR code:

```sh
oathtool --totp -b '<SECRET_FROM_ENROLLMENT>'          # brew install oath-toolkit
python3 -c "import pyotp; print(pyotp.TOTP('<SECRET_FROM_ENROLLMENT>').now())"  # or, with pyotp installed
```

`compose.override.yml` adds build contexts and is picked up automatically,
so `docker compose build` works from a checkout and overrides the published
images with locally built ones.

### Exercising the demo verb locally

The `demo` verb works the same way against a local `go run` server as it
does in the Docker staging setup "Running a staging instance" describes
above — same subcommands, same `DEMO_MODE=true` gate — just with `--db`
and `--as` as global flags before the noun instead of following
`/wordleland`:

```sh
export DEMO_MODE=true
go run ./cmd/wordleland --db ./db.sqlite --as you@example.tld demo seed --players 10 --days 30
go run ./cmd/wordleland --db ./db.sqlite --as you@example.tld demo tick
go run ./cmd/wordleland --db ./db.sqlite --as you@example.tld demo clear --apply
```

`rm ./db.sqlite` between runs for a clean slate. Unset `DEMO_MODE` to
confirm the verb refuses to run without it — the same lockout "Running a
staging instance" relies on to keep it off a real deployment.

## Contributing

All changes go through a branch and a pull request.

Branch naming follows Conventional Commits prefixes:

- `feat/` — new feature
- `fix/` — bug fix
- `docs/` — documentation only
- `chore/` — maintenance (deps, config, tooling)
- `refactor/` — code restructuring without behaviour change

Commit messages:

- Subject line: `<type>(<scope>): <short imperative summary>`, no period.
  `<type>` matches the branch prefixes above. `<scope>` is the affected
  package or area (e.g. `web`, `bridge`, `ingest`, `cli`, `store`, `wordle`,
  `stats`, `auth`, `config`); omit it for repo-wide changes with no single
  owning area.
- Body with bullet points for non-trivial commits, describing only what
  changed in the repo.
- The body explains the reasons, not the sequence of work. Nobody reading it
  later needs to know what was tried first and abandoned, and a change that
  was made and then reverted does not belong in the history at all.

**Every commit builds on its own.** Not just the tip: `go build ./...` has to
pass at each one, or bisecting is guesswork. A rename spread across packages
is where this breaks — the commit that moves a package must not leave an
earlier one referring to it.

**The history `main` gets is the story of the change, not the story of
reaching it.** A pull request usually goes through review rounds — a fix
corrected, an approach reworked, a test adjusted after the first one missed
something. None of that belongs in `main` once the PR merges: amend or
squash before merging, so what lands is the commits above — one coherent
change each, building on the last — not a transcript of how review went.
