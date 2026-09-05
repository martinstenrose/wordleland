# Decisions

Why Wordleland is built the way it is. The code is authoritative on *what* —
the schema, the routes and the CLI describe themselves, and the comments
carry the local reasons. This file holds what the code cannot: findings from
data that is not in the repository, arguments that span several packages, and
the things deliberately not built.

If a claim here can be checked by running something, it should not be here.

## Findings from the real history

These come from analysing the group's actual chat export and score sheet. The
export is **not in the repository** — it is a log of private messages — so
this analysis cannot be redone from anything here. That is the only reason
these numbers are written down.

**The thousands separator is a comma or U+00A0, never a plain space.** By byte
inspection: 664 NBSP against 681 comma, with no narrow (U+202F) or thin
(U+2009) space anywhere. Normalising all three is insurance; NBSP is the one
that must work. The space *after* the word `Wordle` is always U+0020.

**Grid squares vary and must never be parsed.** ⬛ and ⬜ both occur, roughly
10,000 and 2,000 times, from dark and light theme; high-contrast mode
substitutes different colours again. Only the header line is matched, and
every grid line simply fails to match. Do not write emoji-aware logic.

**Hard mode is about half of all results, and it splits by player rather than
spreading across the group.** A third of the roster plays it almost
exclusively (87–96% of their results); the rest are at zero. So "hard mode
only" is not a refinement — it removes most of the board. A leaderboard
mixing the two is comparing scores from two different games, which is why the
filter is a primary view and why the ranked list is expected to be short when
it is on.

**Display names are not distinct.** The roster has contained two members whose
names differ by one letter, and anyone can change their own. This is why
players carry a slug, and why identity resolution never uses a display name.

## Scores and statistics

**Storage records the outcome; the "7" convention lives only in computation.**
A failure is `solved = 0, guesses = NULL`, a missed day is the absence of a
row, and the 7 never touches the database. Counting X as 7 is a display
toggle, on by default.

**A failure stores no guess count, rather than six.** Six is defensible — the
player did make six guesses — but the column means how many it took to
solve, and it did not. More practically, 6 is already a valid *solved* score,
so storing it for a failure would give one value two meanings and leave
`solved` as the only thing telling them apart: every query would have to
carry it forever, and the guess distribution would file failures in the
"solved in six" bucket unless every read remembered not to. NULL makes that
mistake impossible instead of something to remember.

**Archive results are ignored when they arrive from the chat.** Wordle puts
an "Archive February 20, 2026" line above an otherwise ordinary share text;
the bridge rejects that explicit marker regardless of how recent the puzzle
is. It also rejects unlabeled results outside the live puzzle window.

Not because they are untrue, but because streaks are computed by walking the
puzzle sequence, so filling a gap **joins two runs into one**. That is the
common case rather than the rare one: people play archive puzzles precisely
on the days they missed. An existing row is protected by the precedence
rule, but a puzzle the player has no row for inserts cleanly.

The fallback window is one puzzle behind and one ahead, not today alone — a
result posted late in the evening, or from a timezone that has already rolled
over, is still today's as far as the poster is concerned. Each of those is a
single puzzle. Two behind would also cover a trailing timezone posting after
its own midnight, both slips at once, and that is not worth the slack: the
puzzle is meant to be posted on the day, late has in practice meant a minute
or two rather than a day and a half, and every extra puzzle of window is
another door an unlabeled archive result arrives through. Anything explicitly
labeled Archive or outside that window is dropped with a log line naming the
puzzle, so it is visible rather than a mystery.

The check lives in the bridge, not in ingest: the CLI and backfill write old
puzzles legitimately, and ingest stays source-agnostic.

**Counting missed days is bounded by the player's own window** — first result
to last, never all puzzles ever. Otherwise anyone who joined late or drifted
away gets a number that means nothing.

**A month counts missed days regardless, and the leaderboard does not.**
They are asking different questions. The leaderboard is a career average
over a window each player defines by turning up, so counting absences is one
way of looking at it and belongs behind a toggle. A month is a competition
over a fixed set of days everybody had, so a day not played is a failure —
without that, the way to win a month is to play only your good days, and
eleven cherry-picked games beat thirty honest ones. The denominator is every
concluded calendar day from the month's first puzzle, even if nobody posted
on one of them. It still follows *count X as 7*, because with a failure worth
nothing there is no number an absence could take either. Today's puzzle is
not a miss while there is still time to play it.

**A month has no minimum-games threshold.** Every concluded day a player
missed already scores as 7, so a short appearance is penalised by the monthly
calculation itself. Today's puzzle remains open and cannot become a missed
day until the day ends. The ten-game rule on the main board is separate and
unchanged.

**Both emailed links share one table and are kept apart by a purpose
column.** A password reset and an address confirmation are the same shape —
single use, an hour long, stored as a hash, tied to a user — so a second
table would be the same columns under another name. What that costs is that
the token space is shared, and the purpose is what stops a confirmation link
setting a password: without it either consumer accepts either token and
burns it. The purpose is bound into the lookup rather than checked after,
so a token of the wrong kind reads as "no such token" and the response is
the one an unknown token already gets.

**A connected bridge is not a working bridge, so the configuration is
verified against signal-cli rather than assumed.** Both ways of getting it
wrong are silent in the same way: signal-cli-rest-api routes
`/v1/receive/{anything}`, so an account that matches no registration
connects, stays connected, reports healthy and receives nothing at all —
and a group id that matches no group receives frames it discards. Neither
logs a word, by anyone.

This is not hypothetical. An account reached production without its leading
`+`, and the bridge sat connected and silent for eight hours with a green
diagnostics page. The cause was a layer further out still: unquoted in YAML,
`+46...` parses as an *integer*, and the sign is gone before the value is
ever templated into an environment file. It looks correct everywhere an
operator would check it.

So there are three defences, and they catch different things. The shape is
checked at boot, where a malformed value refuses to start. The *meaning* is
checked at runtime against `/v1/accounts` and `/v1/groups`, because only
signal-cli can say whether a well-formed value is the right one; that check
retries, because signal-cli routinely starts after the app, and it is
reported rather than fatal, since a misconfiguration is not fixed by
restarting. It also repeats hourly, because nothing here has to be edited
for a working bridge to stop working — being removed from the group
produces the same connected, healthy silence a wrong group id does, long
after the startup check has passed. It speaks only when the verdict
changes: an hourly line saying everything is still fine teaches a reader to
skip these, and an hourly line saying it is still broken buries the one
that said it first.

**Diagnostics shows the watched account and group in full.** The account was
masked at first, on the reflex that a phone number should be hidden. The
reflex was wrong here: the page is behind `requireAdmin`, the reader is the
person who set the value, and the row exists so it can be compared against
the environment file it came from — which is exactly how a missing leading
`+` gets spotted. A mask makes the one job harder and protects nobody who
could reach the page anyway.

The group id is shown for the same reason, and is safe for a further one:
it is an identifier, not a credential. Signal derives it from the group
master key, and the master key — the thing inside an invite link — is what
grants access. The id names the group and admits nobody to it.

Alongside it, when verification has confirmed the group, the name signal-cli
reports for it. That is the part a human can check. A matching id proves two
strings are equal; a name proves the account can actually see the group
somebody meant, which is the question being asked. It is shown only when
confirmed — claiming a name from configuration alone would assert precisely
what the row exists to establish.

The verdict row states the two claims rather than the authority behind them.
"Confirmed by signal-cli" was true and said almost nothing: it named who
vouched without naming what for, leaving a reader to trust a row they could
not check. It now says the account is registered and a member of this group,
which are the two facts checked and the two ways this can be wrong. It also
says when it last checked, because an hourly re-check reassures nobody who
cannot see it happening, and a verdict from nine hours ago describes a
configuration nothing has asked about since.

**Silence is measured from the last evidence the subscription works.** A
message if one has ever arrived; otherwise the moment we connected. The
check used to be skipped entirely until the first message, reasoning that a
fresh deploy has no baseline and failing on it would make every deploy look
broken. Half right — and it is why the eight hours above went unremarked:
a bridge that connects and never receives anything was never questioned at
all, so the one check that could have named the silence was the one being
skipped.

Two limits, because the two silences are not equally suspicious. A long
quiet spell on a bridge that has been delivering is evidence about the
group; silence on one that has delivered nothing since connecting is
evidence about nothing, and the ping handler keeps the read deadline alive
whether the subscription works or not. So 36 hours once something has
arrived, and 6 before — short because the clock counts frames rather than
results, and receipts, typing indicators and reactions all count. An active
group produces one within minutes.

This is a warning, not a liveness failure: it feeds the diagnostics page and
not the probe, for the same reason a disconnected bridge does not fail it.

**Hard mode filters, it never weights.** A 4/6 counts as 4 in either mode.
A handicap would mean inventing a conversion factor with nothing to justify
it; filtering gives the comparison without that problem — same arithmetic,
smaller population.

**Streaks and missed days are computed from the full history whatever the
filter is.** Both are statements about absence, and filtering manufactures
absences: a player with 169 hard-mode games and 9 normal ones would have
those 9 read as missed days, breaking their streak nine times for games they
actually played. Averages and distributions are a population question and
filter correctly; absence is not.

**Eligibility reasons are computed against the unfiltered history, always.**
Otherwise each reason means something different depending on a toggle: under
a hard-mode filter, players who never play hard mode have no games in the
filtered set, so the first rung would call them `inactive` — which means they
left the group. A player with 176 games would carry a reason that is untrue.

**A filtered board lists only the filtered population.** Players with no games
under the current filter are left out rather than shown as unranked. The
header says how many are excluded, so the omission is visible.

**Form needs ten games in the window.** Below that it is undefined and renders
as `—` rather than a number computed from four games. The window is counted in
puzzles, not days — Wordle issues one a day, so they coincide, and the puzzle
number needs no clock.

## Identity and ingest

**Identity is the sender's account UUID, never their profile name.** A name can
be changed at any time, which would silently break the mapping, and another
member could set theirs to the same string. The UUID survives both a profile
rename and a change of phone number. The last name seen is kept only so a
human can tell which UUID is whom.

**Phone numbers are never stored or logged**, including at debug level. The
envelope carries them; only `sourceUuid` is read.

**An unclaimed sender's results are held in full, not counted.** Identities
cannot be seeded from the source data — it has display names, not UUIDs — so
without this, onboarding someone means digging their UUID out of the logs
*and* losing every result they posted before being claimed.

**An unclaimed sender is held, not an error.** Nothing was lost and the
result becomes real when the sender is claimed, so no caller needs a special
case and monitoring does not read a new player's first message as a failure.
The tradeoff is real: a misconfigured bridge — wrong group, unexpected
senders — also looks healthy from outside. That is what Admin → Diagnostics
is for, and why it leads with when a result last arrived rather than with
connection state.

**A human-entered value always beats an automated one.** A token write may
overwrite a row only where `entered_by IS NULL`. Deleting a result gives up
that protection for the pair, so a later token write will be accepted.

**A suggestion on the pending screen requires an exact name match.** A fuzzy
guess would attribute one player's scores to another, which is worse than
offering nothing.

**`active` is membership, not recency, and is never derived.** Whether someone
quit is not inferable from data — three quiet weeks is a holiday or a
departure, and only a human knows which. Recency is already computed as an
eligibility reason; a job flipping this flag would store what the queries
compute, with the added failure mode of being wrong.

**Posting again reactivates a retired player**, on live ingest only. Replayed
results do not: they are historical and say nothing about the present. Nor
does backfill, whose input declares membership explicitly.

## Authentication

**Hand-rolled, and it stays that way.** No self-registration, no OAuth. Every
account is created by an admin, claimed from an invitation an admin sent, or
bootstrapped on an empty database.

**Two-factor is required of admins, optional for everyone else.** An admin can
rewrite the roster and read the activity log; a player can see a scoreboard.

**An enrolling secret is held pending until a valid code proves it.** A
mis-scanned QR code therefore cannot lock anyone out.

**Enrolment is for accounts that have no second factor, and a session that
has not cleared one cannot reach it.** Reaching the enrolment page needs only
the password, and confirming it overwrites whatever secret was there and
revokes the recovery codes with it — so a stolen password alone would
otherwise replace the second factor and delete the way back, which is the
whole of what the second factor is for. An account that already has one is
sent to the prompt for the one it has. There is deliberately no self-service
way to re-enrol: the routes back are proving the current secret, spending a
recovery code, or an admin running `reset-2fa`.

**Rate limiting lives in memory, not the database.** DB-backed counters would
turn every failed attempt into a write, and writes serialise in SQLite, so a
flood could stall ingest or a CLI command behind `busy_timeout`. The limit is
checked before hashing, so a blocked attempt costs nothing, and concurrent
argon2 calls are bounded separately — at 64 MiB a hash, an unthrottled login
endpoint is a memory-exhaustion DoS whether or not anything is ever guessed.

**Every prompt that takes a password is limited, not only sign-in.** Changing
the password from settings asks for the current one, which makes it a second
door to try a password on and a second way to spend 64 MiB a request. It
counts under its own key rather than sign-in's: sharing one would let a
signed-in tab lock its owner out of the sign-in form.

**The two-factor budget is keyed on the account, not its address.** Sign-in
has to key on the address, because that is all it holds before the account is
found. By the time a code is being checked the account is known — and keying
on a field the account holder can edit meant a password alone bought an
unlimited supply of guesses at the second factor: exhaust the budget, change
the address, start again. The row id also cannot drift if the rule for
normalising addresses changes.

**Recovery codes share the two-factor rate limit** rather than getting their
own, which would double the guesses an attacker gets at one account. They
carry 80 bits, which is what makes storing only a SHA-256 hash safe: there is
no low-entropy guess to grind, so the slow KDF passwords need buys nothing.
They are revoked whenever the secret is replaced or reset — a code minted
against an old secret is a way past the new one, and replacing a compromised
enrolment has to mean the codes too.

**Emailed links are built from `APP_URL`, never from the request `Host`.**
Otherwise someone could request a reset for another account with a forged
header and have the link point at a server they control.

**Losing `TOTP_KEY` makes every enrolled secret unrecoverable.** It belongs in
the backup routine. The CLI's `reset-2fa` is what makes that survivable
rather than fatal.

## Access

**The share link is a capability URL, not a secret that hides the app.** It
grants read access and is never a route to anything authenticated. It is
namespaced under `/share/` so a regenerated slug can never collide with a real
route — a fixed prefix removes that class of bug instead of relying on the
random generator to avoid reserved words.

**The login form is at the bare hostname and is therefore exposed to anyone
who finds the domain.** That is ordinary for a self-hosted service, but it
means the auth defences are the whole of the protection rather than a second
layer behind a secret URL. The reason to keep them strict is not the value of
what is behind the login — it is a scoreboard — but that this auth code is
hand-rolled and has had far fewer eyes on it than an established project's.

**A signed-in non-admin gets 404 on admin paths, not 403.** The area is not
something they are being told they cannot have.

## Staging and demo data

**`demo clear` deletes players outright, the one place in the codebase that
does.** Everywhere else, removing someone from the group is
`players.active = false`: their history stays, because it happened. Deletion
was ruled out for the real app for exactly that reason — retiring is the
honest operation, and a hard delete of `players` cascades to `results` and
`player_identities`, taking a real history with it.

The `demo` verb is the one place that reasoning does not apply, because there
is no history to lose: everything a DEMO_MODE instance holds was invented by
`demo seed` in the first place, including the retired player and the held
pending senders. There is deliberately no column marking who is "demo data"
and who is not — the gate is DEMO_MODE itself, checked once for the whole
verb rather than per player, because a marker column would imply demo and
real players could coexist in one database, and they cannot: a staging
instance is entirely synthetic or it is not a staging instance.

This is also why `demo clear` refuses to run without `DEMO_MODE=true`, and
why `serve` warns at boot when it finds the flag set. The failure mode a
one-off marker column invites is a real deployment where someone sets
DEMO_MODE temporarily, generates a few test players to see the board render,
forgets to unset it, and later runs `demo clear` expecting it to no-op —
instead it deletes the real roster, because nothing distinguished it. Gating
the whole verb on one piece of configuration, checked in one place, is what
keeps that mistake from being reachable: the verb simply does not exist on a
deployment that has not deliberately declared itself synthetic.

**Seeded history is written through `internal/ingest`, not directly into
`results`.** The precedence rule, the pending-sender hold, and puzzle-number
derivation are the same code a live Signal post goes through, so a seeded
board exercises the real paths rather than a shortcut that could drift from
them. It is also why the held pending senders are genuine `StatusPending`
results from `ingest.Apply`, not rows written straight into
`pending_results`: the Pending admin screen is showing exactly what an
unclaimed sender's arrival looks like.

**Hard mode, miss days and guess counts are shaped, not uniformly random.**
An evenly-random board would not exercise the same code paths a real one
does — see "Hard mode is about half of all results" above. `internal/demo`
reproduces that split per player, keeps guess counts centered on 4 with an
occasional failure, and reserves three roster positions (unbroken streak,
a player who stopped, a retirement) so the callouts and admin screens have
something to show immediately after seeding rather than depending on enough
random days to eventually produce one.

**Persona traits are keyed on the player's name, not a database id.**
`tick` has to reconstruct the same `HardModeRate`/`MissRate`/daily roll a
player had during `seed`, days or months later, without persisting
anything beyond what `players` already stores — so `PersonaFor` and
`DailyRNG` hash the name itself. `players.name` carries no uniqueness
constraint (only `slug` does), so two players can end up sharing a name if
`demo seed` is run a second time without `demo clear` first — at which
point their behaviour becomes fully correlated, since the same name hashes
to the same persona. This is a real, if narrow, consequence of the
stateless design rather than a bug to fix: the documented workflow already
says not to re-seed onto an existing roster (see the "Running a staging
instance" section of README.md), and a fix would mean the roster knowing
its eventual, database-unique slug before any player row exists, which it
cannot today.

## Deliberately not built

- **Self-report in the browser** — a player filing their own result, by form or
  by pasting share text. `players.user_id` and the shared parser exist for it;
  no route does it. Manual entry is the CLI's job. This is the largest thing
  the original spec promised that v1 does not do.

  When it is built, the linked login is what grants it: an account linked to a
  player may add and edit that player's results and nobody else's. There was a
  `user_permissions` table for this, dropped in 0007 — the board is not gated,
  so its `view` capability decided nothing, and `players.user_id` already says
  who may report, is already managed on the admin screen, and is already
  described there as meaning exactly that. Per-capability control can come
  back as its own migration if it is ever wanted, with a reader attached.
- **Undo on the pending-results screen.** Undoing a discard is impossible once
  the rows are deleted, and undoing an assignment means un-replaying scores
  already on the board.
- **Admin UI for users, tokens, result corrections and the share slug.** The
  CLI remains the fallback and the bootstrap path regardless — it is the only
  thing that works before a user exists.
- **Activity detail beyond what a row shows** — no filtering by player.
- **Groups**, letting one player appear in several. Nothing in the schema
  assumes a single group, so it is purely additive.
- **Passkeys.** `users.handle` exists from the start specifically so this can
  be added without invalidating anything: it must be opaque, and changing the
  id strategy later would invalidate existing credentials.
- **OIDC**, and gating the whole board behind login.

## One process

**The Signal bridge runs inside the application rather than beside it.** It
was its own container, posting to `/api/ingest` over the network with a
token. Merging it removed the token, the HTTP hop, and a setup step — and
`/api/ingest` stays for curl and any future bridge, so nothing was given up
except being our own client.

What it cost: a panic in the bridge would take the board down, so the
supervisor recovers and restarts it, and gives up after repeated crashes
because a panic loop is a bug rather than bad luck. And the container no
longer goes unhealthy when Signal is unreachable, which used to be the
signal that something was wrong.

**`/healthz` answers one question: would restarting help?** The database
being unreachable, or a bridge the supervisor abandoned — yes. A bridge
disconnected and retrying — no, and failing the probe there would take down
the board because a third-party container is down.

**The same reasoning keeps `depends_on` at `service_started`.** Waiting on
`service_healthy` would mean the board, sign-in and manual entry are all
unavailable until a third-party container reports healthy — and it can stay
red indefinitely on an unlinked or expired registration, so the whole app
would never start. The bridge reconnects on its own with backoff to a
minute, so the only thing the wait buys is a quieter log at first boot.
`depends_on` orders one `docker compose up` and nothing else besides:
after a restart of the Signal container alone, or a host reboot where the
restart policies bring things back independently, it does not apply at all.

**So freshness is reported, not probed.** Admin → Diagnostics leads with when
a result last arrived, because the failure that costs a season of scores is
a bridge connected to the wrong group: green on every connection indicator,
delivering nothing. A warning follows an admin around the area rather than
waiting on a page nobody opens.

**One janitor sweeps every kind of expired state, rather than a job per
table.** Sessions, password-reset tokens, rate-limit buckets and — when
`PENDING_RETENTION` is set — held results all expire, and for a long time
none of them were reaped. Nothing was ever *wrong*: every check treats an
expired row as absent, so the cost was unbounded growth rather than bad
answers. One goroutine on a ticker covers all four, because four schedules
would be four things to reason about for deletes that cost nothing at this
size.

**Its interval is not a security setting**, which is worth stating because it
looks like one. Each sweep removes only what the code already treats as
absent — the limiter deletes a bucket using the same staleness test `Allow`
uses to ignore it — so sweeping cannot hand back an attacker's attempt
budget or shorten a lockout, however often it runs. A longer interval only
holds dead rows longer. What the sweep does bound is the rate-limit map,
whose client-address key is attacker-controlled and would otherwise grow
with every distinct address ever seen.

## Announcing the month

The bridge's first step from receive-only to bidirectional: posting the
month's winner back into the group when a month closes. Deliberately small
— one message, once a month, no significance threshold and no memory of
previous standings — because that is exactly what makes it safe to build
before the larger idea it is a step toward, announcing rank changes as they
happen.

**The scheduled trigger is noon on the first day of the new month.** This
gives late closing-day posts the morning without making the announcement
depend on every active player filing a result: somebody missing that puzzle
must not block the group indefinitely. The schedule uses the deployment's
local timezone, the same basis as puzzle dates.

**A later live result is the catch-up trigger.** If the app was offline at
noon or the scheduled send failed, the next accepted result checks whether
the previous month still needs announcing. Before noon that check is a no-op.
Ordinary conversation is not a trigger: if sending succeeded but recording
failed, receiving the bot's own announcement must not immediately send
another copy. Explicit Archive shares and older back-dated results are also
excluded, so replaying an old puzzle cannot make the bot speak.

**The catch-up check only ever looks at `previousMonth(now)`, deliberately,
not at every unannounced month back to some watermark.** The gap that leaves:
if the scheduled send fails and the group also goes completely silent for the
rest of that month — no live result at all to trigger the catch-up check —
the next month's own scheduled run only examines its own previous month, and
the missed one is never revisited. Accepted rather than fixed, because a
group inactive enough to fail both conditions at once is not the case this
feature is for, and the alternative — bounding the check by the most recent
month ever recorded as announced, so it can walk forward and post several
months at once — creates a stranger failure than the one it solves: the
first time the feature runs against a board with months of prior history, or
after any long-enough gap, it would post that entire backlog into the group
in one burst, which reads as far more surprising than one quiet month.

**A month with no results in it at all gets silence, not the board's "no
scores to rank" line.** The board is read on request; this is pushed
unprompted, and announcing a quiet month reads as the bot scolding the
group for it. Not recorded as announced either — if results appear later
through a correction, the next live message is free to announce them
rather than having already given up. Months no longer have a minimum-games
threshold (see "A month is a competition..." above), so this used to also
cover a short appearance that fell below it; it now only fires when
literally nobody posted.

**The send happens before the record of it, not after.** Recording first
and then failing to send would silently and permanently drop that month's
announcement — the far more likely failure, since a monthly HTTP call to a
self-hosted container has more chances to be down than this code has
chances to crash in the few milliseconds between a successful send and the
write that follows it. The accepted risk is the reverse: a crash in that
narrow window can produce a duplicate post on restart. Duplicate beats
silent loss for a message whose only job is telling people something nice.

**The announcement is written in one fixed language for the whole group**,
`SIGNAL_LOCALE` (default English), unlike every other piece of text in the
app, which is written for one signed-in reader with their own stored
locale. A group chat is not any one member's message, so there is no
per-recipient choice to make here the way there is for a page render or an
email.

**The catalogue of translated strings moved out of `internal/web` into
`internal/i18n`** so this feature could reuse strings the board already
carries — month names, the tied-name conjunction, number formatting —
without a second copy that could drift from the first. `internal/bridge`
cannot import `internal/web` — `web` already imports `bridge`, to hold the
running supervisor for the diagnostics page — so the shared catalogue had
to move to a package neither depends on. What did not move: the small
switch that picks a tie, a margin or an "alone" sentence for a given
month. It exists once in `internal/web/months.go` and once in
`internal/announce`, deliberately, because the two format for a browser
and for a chat message respectively, and a shared type for ten lines of
branching would cost more to agree on than reading both sides once when
either changes.

**The winner-line sentences themselves are not shared, on purpose:
`months.line.*` on the board, `announce.line.*` in Signal.** They started
as the same keys, but the chat message wants things the page does not — a
🏆, and the winner's average folded into the sentence — and the board
already shows the average as one of the four stat figures beside the
winner's name, so repeating it in the page's prose would say it twice.
Reusing one key family for both would mean either putting a trophy emoji
into on-page text, or branching the rendering on which surface is asking,
which defeats the point of a shared string. Everything else in the
catalogue stays one shared set of keys; this is the one deliberate
exception, not a precedent for splitting further without the same reason.

**Display numbers follow the selected locale on every surface.** Swedish
uses a comma as the decimal separator and spaces between thousands; English
keeps the application's previous decimal-point and ungrouped forms. The
formatter lives beside the shared catalogue so a monthly average cannot read
one way on the board and another in Signal. Name lists use the same catalogue
for their final conjunction, rendering Swedish `och` instead of the
language-neutral ampersand used by English.

**Turning it off is a separate switch from configuring the bridge at all**,
`SIGNAL_ANNOUNCE_MONTHS`, defaulting on. The bridge's own on/off state
already answers "does this deployment talk to Signal"; this answers "does
the bot ever speak in the group," for someone who wants results flowing in
without the bridge ever posting back.

## CI and security scanning

**CodeQL's `go/log-injection` alerts on `internal/web` are false positives,
dismissed on the security tab rather than suppressed in code.** Every site it
flags — the request logger, panic recovery, template render and write errors
— logs `r.URL.Path` through `slog`'s structured attribute API (`"path",
r.URL.Path`), never by concatenating it into a message string. `slog` quotes
any attribute value containing control characters — newlines, CR, ANSI
escapes — for both the text and JSON handlers, so a crafted path cannot forge
a second log line or inject terminal escapes. Verified directly: logging a
payload containing both a newline and a forged second entry came back as one
escaped, quoted line, in both handlers.

Each alert is dismissed as false positive, with that reasoning, rather than
either alternative. An inline `// codeql[go/log-injection]` suppression
comment must stand alone on the line *before* the flagged expression — a
trailing comment on the same line changes the line's content, which changes
the alert's hash and opens a *new* alert instead of closing the old one. That
is easy to get wrong (it happened once, in the PR that added this entry) and
has to be repeated at every call site. A repository-wide query exclusion
would need no per-site action at all, but would blind CodeQL to a genuine
log-injection bug anywhere in the repository, including code that does not
yet exist — a future log line built with `fmt.Sprintf` instead of an `slog`
attribute, say, which this same query would be right to flag.

## Logging

**A filed result logs at info, not debug.** Diagnostics answers "what is the
bridge doing right now," for someone with a browser open; `LOG_LEVEL` is for
someone with neither, at a terminal on the box. A misconfigured bridge once
ran silently for eight hours before either the group or the results were
missed. At debug, the same silence is not distinguishable from a bridge that
was never running at all — nothing in the log says whether it is quiet or
dead. Info is what makes the two tell apart: a working bridge produces a line
per result, so a stream with nothing in it for hours is itself the evidence
something is wrong, without anyone having had to switch anything on first.

**What may be logged, and at which level, follows the trust boundary
"Identity and ingest" above already draws — the audience is what matters,
not the level.** The log's only reader is whoever is deploying and operating
this instance, and that person already has full access to the database and
the admin dashboard: a resolved player's id and slug, and the sender's
account UUID and current display name, name nobody a log line tells them
about for the first time. There is therefore no privacy reason to hold any
of it back to debug — a bridge that files a result at info should say whose
it was, or the line is a heartbeat with nothing to show for it. `filed a
result` and the other outcomes in `forward.go` log the resolved
`player_id` and `slug` together rather than either alone: the slug is what
a human reads, the id is what survives the slug changing under it.

Two things stay excluded regardless of level, because the reason is not
about the audience being wider than the admin — it is about the field
itself. The phone number never appears, because the schema deliberately
never stores it either: unlike the UUID it identifies a person outside the
app and does not survive a number change, so there is nothing to log that
is not itself a mistake. And a message's text is never logged, because it
is somebody else's conversation, sent to a Signal group and not to this
application; signal-cli-rest-api's own container log already carries the
full envelope for as long as that container lives, and that is the right
place for it to exist, not a second copy with a different lifetime and a
different set of hands with access to it.
