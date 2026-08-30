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

**Results for old puzzles are ignored when they arrive from the chat.** The
group posts archive results — an "Archive February 20, 2026" line above an
ordinary share text the parser matches exactly — and those must not reach
the board.

Not because they are untrue, but because streaks are computed by walking the
puzzle sequence, so filling a gap **joins two runs into one**. That is the
common case rather than the rare one: people play archive puzzles precisely
on the days they missed. An existing row is protected by the precedence
rule, but a puzzle the player has no row for inserts cleanly.

The window is two puzzles behind and one ahead, not today alone — a result
posted late in the evening, or from a timezone that has already rolled over,
is still today's as far as the poster is concerned. Anything outside it is
dropped with a log line naming the puzzle, so it is visible rather than a
mystery.

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
eleven cherry-picked games beat thirty honest ones. The denominator is the
days the group played, not the calendar: a day nobody posted is not a day
anybody missed. It still follows *count X as 7*, because with a failure
worth nothing there is no number an absence could take either.

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

**Rate limiting lives in memory, not the database.** DB-backed counters would
turn every failed attempt into a write, and writes serialise in SQLite, so a
flood could stall ingest or a CLI command behind `busy_timeout`. The limit is
checked before hashing, so a blocked attempt costs nothing, and concurrent
argon2 calls are bounded separately — at 64 MiB a hash, an unthrottled login
endpoint is a memory-exhaustion DoS whether or not anything is ever guessed.

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
