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
*Reversed for the grid's own rows by* The squares under a result are kept,
*below: the score still comes from the header alone, and the squares are
read after it, in any theme's colours, and dropped when they disagree.*

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
on one of them. Today's puzzle is not a miss while there is still time to
play it.

**The two counting rules are independent, and used not to be.** Counting
absences was gated behind counting X as 7, on the reasoning that with a
failure worth nothing there is no number an absence could take either. There
is. Seven is what a Wordle is worth when it was not solved, and whether
somebody attempted it is a separate question from whether they turned up: "a
failure does not count against you, but not playing does" is a rule somebody
can want, and it was not expressible.

All three places that fill in an absence follow the same rule, so all three
changed together — the board's toggle, the month's own denominator, and
Today's thirty-day form window. Leaving the last two gated would have meant
one query string producing a board that counts absences beside a month table
that quietly does not.

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

**Hard mode breaks a tie on the day, and only a tie.** Two people on the same
score are ordered with hard mode first; a better score in normal mode still
leads a worse one played hard, so a 3 stays ahead of a 4*. This is not the
handicap the paragraph above rules out. A handicap would need a conversion
factor — how many guesses hard mode is worth — and there is nothing to derive
one from. Ordering two results that are already equal needs no such number:
the arithmetic is untouched, and the only question left is which of two
identical scores to print first. Alphabetical was an arbitrary answer to
that; hard mode is a real one.

It applies to a shared failure too. Two X's are the same result as much as
two 3s are, and an exception there would be a second rule to remember for no
gain. It orders the day's list on Today and the names in the Signal recap's
tied best, because both read the one order `stats.ComputeToday` produces. The
count of who holds the day's best is untouched: a tie is still a tie, and the
recap still names everyone in it.

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

**Form needs ten played games in the last 30 puzzles.** Below that it
renders as `—`. Today's table is numbered by form rank, while the top-three
cards show overall board rank. Equal form scores share a form rank, and
missing form has no form rank.
Board ranks, eligibility and banter retain their normal rules. Today counts
completed missed days as 7, including gaps after the last submitted result.
An unplayed today stays open until the day ends, and days before a player's
first game are not absences. The minimum counts actual games, so missed-day
penalties cannot qualify a player. Hard-mode filtering cannot turn an
ordinary game into a miss. Windows count puzzles, not timestamps — Wordle
issues one a day.

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

**The squares under a result are kept, as letters, and only when they agree
with the score.** Since migration 0016 a result carries its grid —
`results.grid`, and `pending_results.grid` for a sender still to be claimed
— so Today and the Puzzle page can draw who got there how. The parser still
takes the score from the header alone; it then reads the run of square rows
under it into `g`, `y` and `n` per letter (`nynnn/ggggg` is a 2), whatever
the sharer's theme drew them in: ⬛ or ⬜, and 🟧/🟦 in high contrast. A grid
that disagrees with its header — the wrong number of rows, a solve that does
not end green, a miss that does — is dropped and the result filed without it,
because a cropped or edited paste is no reason to lose a score. The API
takes the same letters as an optional `grid` and refuses one that
disagrees, since there a caller could send one.

Chat around the grid is not part of it: text before the header, a line of
comment after the last row, and a comment typed on a row's own line after
its five squares ("🟩🟩🟩🟩🟩 puh 🥲") are all read past. The last one used
to cost the grid — the line was not pure squares, so the grid came up a row
short and no longer agreed with its score. Text *in front of* a row's
squares, or a sixth square, still makes the line not a row: the leniency is
for what follows five squares, not for finding squares in a sentence.

A later write replaces the grid when it brings one. When it does not — a
correction by hand, a score through the API — the stored grid is kept only
if the score is unchanged: squares drawn for a 4 must not sit beside the 3 a
correction made of it. Nothing is backfilled: the chat history is not
re-read, so the board has grids from the day this shipped, and the pages say
so ("Grids recorded since #N") rather than showing earlier days as bare.

## Authentication

**Hand-rolled, and it stays that way.** No self-registration, no OAuth. Every
account is created by an admin, claimed from an invitation an admin sent, or
bootstrapped on an empty database.

**Two-factor is required of admins, optional for everyone else.** An admin can
rewrite the roster and read the activity log; a player can see a scoreboard.

**An enrolling secret is held pending until a valid code proves it.** A
mis-scanned QR code therefore cannot lock anyone out.

**A session that has cleared the password and not the code cannot reach
enrolment.** Confirming an enrolment overwrites whatever secret was there and
revokes the recovery codes with it, so a stolen password alone would
otherwise replace the second factor and delete the way back, which is the
whole of what the second factor is for. A half-signed-in session is sent to
the prompt for the secret the account already has.

**A session that has cleared both may change its own two-factor, and the
password is what makes that safe rather than the session.** Both things an
account can do to its own second factor — rotating the secret, and turning
it off — ask for the password on top, for the reason changing the
password asks for the current one: a borrowed screen has already cleared both
factors, and the password is the one thing it does not carry.

Replacing is the ordinary case and this page used to refuse it. A new phone
or a lost one is a thing an enrolled account is supposed to survive, the
settings screen had offered it all along, and the link landed every one of
those people back on Today with nothing said.

It is the secret that is rotated, not an app. An account holds one shared
secret and an authenticator app is a holder of it, so any number of apps can
show the same code — which is why the control is named for the secret, and
says "rotate", and the copy tells you to scan it into every app you mean to
use. Named for the app it read as "add another one", which is the one thing
it does not do: a new secret silences every app still holding the old one.

The word is "secret" and not "key" because TOTP_KEY is already the key this
server encrypts every one of them with, and the admin settings screen shows
it. The standards use it too: RFC 4226 calls it the shared secret, and the
otpauth:// URI carries it as secret=. The old secret keeps working
until the new one is confirmed, which is what the pending-secret design was
already for, and promotion then discards the recovery codes along with the
secret they were minted against — so the flow ends where a first enrolment
ends, handing over a new set.

*Since the design's fifth pass the setup is drawn inline, in the Two-step
card of the settings page itself (`/settings?setup=totp`), with or without a
script; the dialog, and this use of `?partial=1`, are gone. See* Settings
and the admin area, fifth and sixth passes. *What follows is the earlier
shape.*

Setting a key up is a step in what somebody is already doing on the settings
screen, not a place to go, so with a script it opens over that screen and the
whole exchange happens there — the code, the password, a rejected code, and
finally the recovery codes, which are shown once and are worth showing where
the reader is already looking. Without one it is the page it has always been,
which is also the page an admin is sent to at sign-in: the link is a link the
whole time, and the server renders the same card either way and is asked only
to leave the frame off. That is the third and last thing `?partial=1` is for.

Turning it off is offered to players and not to admins, because the admin
area is gated on two-factor and a control that switched it off would make
"required for admins" a suggestion. The template decides what is offered and
the handler decides what is allowed, because only one of those is reachable
by typing a URL.

Neither deletes the account's other sessions, unlike an admin running
`reset-2fa`. That command is for an account that may be in the wrong hands;
these are somebody deciding about their own from inside it, and every session
they would delete proved both factors when it was granted — no weaker after
the change than before it, and one of them is the tab the decision was made
in.

Losing every app holding the secret, without being able to sign in at all,
is still the
admin's job: the routes back are spending a recovery code, or `reset-2fa`.

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

## The look

**The design lives outside this repository.** The palette, the type and radius
scales, the component treatments and the shape of every screen come from the
Stenröse design system, a Claude Design project, against which Wordleland was
drawn page by page before any of it was built here. `internal/web/static/app.css`
is that design expressed as tokens; `internal/web/static/README.md` documents
each one.

What follows from that: a value in the token block that looks arbitrary
probably is not ours to re-pick on its own, and a change to the look is worth
making there first. What does *not* follow is that the design is authoritative
over this repository's constraints — four things were deliberately taken
differently:

- **Its icon font.** The design pulls Material Symbols Outlined from Google
  Fonts. The glyphs are the same here, drawn from their path data as inline
  SVG by a template function (`symbols.go`): a page that reaches a third party
  to finish rendering is a page this app does not control, and one more party
  watching whoever reads the board. It also costs a page only the glyphs it
  draws, where the font is megabytes for the few dozen used. A glyph with a
  filled form carries both paths, and the stylesheet fills whichever is
  current. The licence, Apache 2.0, travels beside the static files.
- **Its fonts.** The body face is the Apple system font, SF Pro, named in the
  stack and never served — Apple licenses it only as the system font — with
  Inter self-hosted as the fallback everywhere else, as the closest match to
  it. Manrope, the design system's own face, went with the glass redesign.
  An Apple device never asks for the Inter file.
- **Its shell.** The prototype is React, and its menus and pages are
  component state. Here every menu is a `<details>`, every choice in one is a
  link, and the theme is three links. See *The navigation floats*, below.
- **The inks on its score ramp.** The ramp itself is the design's: the accent
  at full strength for a 2 and at 78, 52, 30 and 14 percent for a 3 to a 6,
  mixed in sRGB into the raised surface (in OKLCH the middle of the ramp
  walks through yellow and comes out olive). A 1 is drawn as a 2; there is
  no stronger green to give it. The design puts the accent's white ink on
  every tile to 52%; measured on the light canvas that is 3.2:1 at 78% and
  2.2:1 at 52%, so the pale half takes the text colour instead — from 78%
  in light mode and from 52% in dark, where the accent is bright. The full
  tile carries white at 4.3:1 in light mode, a shade under 4.5; the digit
  is bold, and the design's green was kept rather than darkened.

**What the menus do not do.** Each opens and closes with no script, and the
browser keeps one open at a time. A press outside one closes it and so does
Esc, but both are `app.js`'s; without it a menu stays open until its own
control is pressed again, and focus is free to leave it. None of them is
marked up as a modal dialog, because none of them is one.

## The navigation floats

The redesign that took the rail out is Apple's Human Interface Guidelines
with a light layer of Liquid Glass: calm, content-first pages, and the
navigation floating over them as frosted glass. It was drawn in the design
project as four sets of mockups — desktop, phone, sign-in, and the pill row —
and built from them here.

- **No rail.** On a wide window the bar is a capsule holding the mark, which
  is the way back to Today, and every view; a search field with its ⌘K; and
  the account. On a phone the capsule shows the page you are on and opens
  the rest, and beside it are two round buttons and nothing else. The admin
  area is not a view: it is one row in the account menu, for the reader who
  has it. The rail's collapse control, its cookie and its `?sidebar=` went
  with it; an old link carrying the parameter is only a link with a
  parameter nobody reads.
- **Glass on what floats, and nothing else.** The bar's pieces, their menus,
  and the figures' half of the sign-in card are glass; every card of content
  is the solid surface. The frosting is painted by each piece's `::before`
  rather than by the piece: a `backdrop-filter` makes its element the box
  fixed descendants are placed in, and About is a fixed panel opened from
  inside the account menu.
- **Nothing is named for the view transition.** An element with a
  `view-transition-name` is a backdrop root — the edge of what a
  `backdrop-filter` inside it can see — so a named bar frosted only its own
  empty box, and the page showed through it sharp. Naming the content alone
  is no better: its picture is drawn over the window's, and a scrolled page
  would lay its rows across the bar for the length of the fade. So the
  whole window cross-fades. The bar is the same pixels on both sides of a
  swap, and the browser's blend of two identical pictures is the picture:
  it holds still anyway. `TestBrowserTheBarIsGlassAndNothingIsNamed` pins
  the two together, because a tidy-up that names the bar again passes every
  test that reads the stylesheet.
- **The page scrolls under the bar.** There is no strip reserved for it; the
  page's first card is padded clear of it. The viewport is `cover`, the page
  runs under the notch and the home indicator, and what must not sit under
  either keeps clear with `env(safe-area-inset-*)`. Nothing is fixed to the
  bottom of a phone, where Safari keeps its address bar. That held against
  the fifth pass too, whose toast floats 92px above the foot of a phone: on
  a phone it hangs under the bar instead. The browser's toolbar wins for
  this app; a native one might decide differently.
- **The account menu** is headed by the linked player's name, or the address
  where there is none, and "Wordleland · N days"; then the theme as a
  segmented control, the language as a row that opens its list in place,
  About, Settings, the admin area and sign out. Settings is not in the
  mockup, whose menu has no way to the account's own settings; it was kept
  rather than lose the page. A reader with no account — a share link, or a
  stranger on the privacy page — has a guest seat in the same place, whose
  menu says they are viewing as a guest, then the theme, the language and
  About, and ends in the sign-in button. The sign-in button, the theme track
  and the language pill that stood in the bar are gone from it.
- **Every page ends in a footer**: the mark, "Wordleland · N days", Privacy,
  About and GitHub. An error page drops the count — how much history exists is
  not for a stranger who typed a wrong address.
- **The sign-in family has no bar.** Its card sits over a tilted field of
  score tiles fading into the canvas, with the language, Privacy and GitHub
  in its footer. The theme is whatever the device has, or whatever was
  chosen inside; there is no control for it at the door. Sign-in itself is
  one card, glass so the tiles show through, with the form on a solid half
  and the group's figures on the other — now including its guess
  distribution, an aggregate like every other figure there. On a phone it is
  the form alone.
- **Transitions are quiet.** 0.2s for a pill lighting or a chevron turning,
  the page's 0.16s cross-fade. The one spring is a press on a piece of the
  bar, which swells it a little and lets it settle, the way Apple's glass
  answers a touch; reduced motion turns it off.

Five things are drawn differently from the mockups, each for a reason
measured or stated:

- ~~**No eyebrow over a pill row's title.**~~ Reversed by *Every page is a
  head over cards*, below: every page carries an eyebrow now, so the title
  stands at one height everywhere again.
- **The phone's Today has no letter tiles.** The phone mockup spells the
  day's winner in tiles over the headline. The headline is not always one
  name — a shared day, nobody yet — and the hero tiles went once already
  (see *Today is the day's result*).
- **The phone's form list keeps its ranks in parentheses** rather than an
  arrow for how far the form rank sits from the overall one: an arrow reads
  as movement since yesterday, which it is not.
- **Column widths are the measured ones.** The form list's two figures are
  72px, not the mockup's 60 and 64: "Snitt 30 d", "Mot snitt" and
  "Media 30 g" clip below that. The page is 1160px of content — the
  design's width, since the sweep before merging; it was 1080 — 40px in
  from the window at a desktop width, and the two lists stand side by side
  from a 1160px window, as before.
- **The stat labels on a player's phone head wrap** rather than clipping,
  and the streak takes the board's own label, "Streak".

## Siblings are a row of pills

*The admin screens left this pattern in the design's fifth pass: a
segmented bar of tabs on a wide window, and on a phone a list of the
sections at `/admin`. The roster still works as below.*

Two places have siblings: the five admin screens, and the roster of
everybody on the board above a player's page. They were a strip of tabs above
a title first, and the strip wrapped to a second row on a phone and scrolled
sideways for fourteen names. Then the title became the control: a heading
that opened a menu of the rest. That fitted at every width and hid what
could just be shown — a list you have to open is a list you have to
remember is there.

So they are a row of pills: every sibling always in view, one press to any
of them. A pill can lead with a rank or a glyph and end with a score tile or
a count. On a wide window the row runs along the foot of a header card, with
step arrows beside it that wrap at both ends, so neither is ever a disabled
control; on a phone the head is a large title on the canvas, and the row
runs to the window's edges and swipes, with a fade at the right to say
there is more. `app.js` scrolls the row so the current pill is in view when
the page arrives — with `scrollTo` on the row, because `scrollIntoView` on
the pill would scroll the page as well. Without it the row starts at its
left end, and every pill is still a link.

One partial draws both cases (`switcher`). A section's pill carries its
glyph and, on Pending, how many senders are waiting — counted on every admin
screen, since a count only the pending screen shows is a count you have to go
there to see. A player's pill carries their place on the board and their
latest result from the last five days: today's once it is in and the one
before it until then, so the row is not blank every morning, and nothing
for a player gone longer, whose tile from last month would read as recent.
Each is built from the list that already existed — `AdminTabs`, and the
board — so there is one place that knows what the admin area contains and
one that knows who plays.

The title starts where every other page's title starts — in the page head,
on the canvas, above the pill row's own card (see *Every page is a head over
cards*). The glyph a heading once carried in front of it, which pushed it
45px in, stays gone.

The players view opens on whoever leads the board. It used to open on an empty
page asking which player to show — the honest answer, at the time, to a view
with no subject — and in use the question had one answer nearly every time and
cost a tap to give it. The whole roster is one press away in the row either
way.

A player's page lives at `/players/{slug}`, not at `/p/{slug}`. The short path
was the only single-letter segment in the application and the only place the
concept was spelled differently from the `/admin/players/{slug}` beside it;
plural also puts the collection and its members under one path, which is what
`/players` redirecting into one of them already implies. The old path answers
with a permanent redirect, under the share prefix as well as without it,
because player links get pasted into the group chat and a link somebody
already holds should not die for a rename.

The roster is the harder case, and the reason its pills carry figures at all:
fourteen bare names in an arbitrary order is a row you have to read, and the
same names in the board's order with each place beside them is the
leaderboard in miniature, which you can aim at before reading. The rank is
withheld below the ranking threshold, as it is everywhere else — this row
would otherwise be the one place a place earned over three puzzles slipped
out, and the place nobody would think to look.

## Today is the day's result, then what it means

*Since the glass redesign the blocks below are cards of their own on the
canvas — the day, four callouts each led by a glyph, and the two lists side
by side at the design's 5:7 — and on a phone the page opens on a large title,
"Today", over the day's card. That reverses the call recorded under "What
went" that the page should not name itself: the rail it would have repeated
is gone, and at a phone's width a page that opens on a sentence reads as a
page scrolled halfway down. It is presentation only — the headline is still
the page's `<h1>` — and a wide window still leads with the day.*

*The second pass moved the page's name into the page head (see *Every page
is a head over cards*), so "Today" is the `<h1>` at every width and the
day's result is the card's `<h2>`. Before the day's first score (the
design's 2f) the day's card, the spread and the results list give way to one
empty card saying nobody has played yet; the form list stays, since it is
about the last thirty days, and so do the callouts. The still-to-submit
names open from the progress count under the headline, and the list grows
the card downward so the headline does not move.*

*The players view with nobody on the board is the same empty card, with no
pills. It used to redirect to the board path it was handed, which was its
own address, and looped until the browser gave up.*

The front page opened on a headline, a row of tiles for whoever had won, and
a wrapping strip of name-and-score pairs. The strip said who had played and
nothing else: not who was ahead, and not whether a 4 was a good day for that
person or a bad one. Both are answered by figures the page already had — the
standing so far, and the distance from that player's own average — and
neither was being shown.

So the day became a list, best first, and the rest of the page was cut to
make room for it. What went, and why:

- **The hero tiles.** The winner's row is one row among the rest now, drawn
  the same way. Two drawings of the same result, one of them larger, is a
  headline for a number already in the list.
- **The three podium cards, and the six-column table under them.** They gave
  three people a paragraph each and everybody else a line, which made the
  table read as an afterthought — and the unlabelled figure beside a name
  meant a game count in the cards and a streak in the table. One row shape
  for everybody is shorter, reads down, and settles that by construction.
- **The trait badge**, with them: the same argument the leaderboard and
  Months had already won. These are rows of figures about a window, and the
  name column is for the name. Last Five went too at the time, and came
  back — see below.
- **A round trip.** The players the board does not rank were fetched with
  `?benched=1` and swapped in by script. The list is small and was always
  going to be rendered, so fetching it was work spent avoiding a
  `<details>`.

Two rules in the new list are worth stating. A miss says "missed" rather than
a distance from an average, because it is off the scale the average is
measured on and "▲ 2.61" would invent one. And a player the board does not
rank is in the day like everybody else but holds no position among the
ranked: numbering them would push everyone below down a place for the wrong
reason.

On a wide screen the results and the form stand side by side, which is what
makes the two lists' measurements load-bearing — a row in one has to sit
level with the row beside it, so they share one rule rather than two that
agree today. The same argument runs through the tables: the board, Months and
the season table under it each measured their own rank column, so three
tables about the same roster put the names in three different places. One
token holds that width now, and the table with no rank takes it as an indent.

The column labels over Today's two lists are their own strings rather than
the board's. The board's headings live in a wide table that scrolls; here a
long label over a 72px column wraps, one list's header grows taller than the
other's, and every row below it is out of step. The widths are the longest
label in either language, measured rather than guessed.

### The form list spells its last five out

The form list drew each player's month as a sparkline. It showed the shape
of the month and none of its scores: a 2 and an X were a dip and a spike,
and which was which took the legend. Five score chips read the same way —
newest on the right, a day not played a gap — and are legible, and they are
the chip the results list draws today's score with, so the two lists share
one vocabulary rather than a tile in one and a line in the other. This
reverses the earlier call above, and for the reason stated there: Last Five
went as one of several figures about a window, and comes back as the one
that says what the figure beside it is made of.

Two consequences are worth knowing. The results list's score shrank to
match — 30px was the largest thing on the page and it is a row among rows
now — and the average moved to sit before the delta it explains, in both
lists, so the right-hand columns read the same way top to bottom. And the
form row is now five chips and two figures either side of a name, all at
fixed widths, so the name is what gives: on a phone the chips stay the
board's own 20px and the figures take their own width, and the two-column
layout starts at 1160px rather than 1000, with the form column the larger
share, because below that the name had nothing left. Both are measured in
the browser suite, since nothing that reads the markup can see a name cut
to one letter.

## Every page is a head over cards

The design's second pass redrew every page on one pattern, and this is it.
A page opens on a **page head on the canvas** — an eyebrow, the page's name
as the `<h1>`, a line under it — with the page's tools or a player's figures
opposite; everything below is **cards**: the pill row, the day, each
callout, each list. The board was the one exception while it was reworked
on its own, which the third pass did. The eyebrow carries what used to be a subtitle under the title —
the puzzle and the date, a player's place and last game, the year on
Months, the window on the grid — so the title stands at one height on every
page, which is what the rule against an eyebrow on some pages and not
others was protecting.

A page with **nothing in it yet** is one card saying so — a glyph, what is
missing, what will fill it, a row of empty tiles — in place of everything
the page would draw, rather than each card drawn empty. Today keeps its
form list under it, which has something to say.

**About** is a dialog, opened from the account menu, the guest's, and the
footer — the same `<details>` in three places, grouped so one is open at a
time. On a phone it is a sheet from the bottom with a grabber. Its close
button needs `app.js` and is hidden without it; the summary closes it
either way. Its body says what the app does, three facts (it reads the
group chat; the score is the average with a miss as 7; hard mode is marked)
and links to Privacy and the source.

**Today** adds a spread — a bar per guess count, today, in the tiles'
colours, and the group's average against its usual over the previous 90
puzzles — and a strip for the month's race: who leads, by how much, the top
three and the way to Months. On a phone the two lists are two tabs over one
place, a segmented control made of two radio buttons and a stylesheet rule,
so they switch with no script. A live redraw lands on the results tab.

**Months** opens on the month in a card of its own: who won or leads, by how
much, and the days left; then **who led each day** as a strip of tiles, one
colour per leader, from a new `stats.DayLeaders` that scores the month day by
day with the same rule as the table. The strip is not a set of links: a
tile's title names the day and the leader, and a day is not a page. The
table drops the columns the design drops — best run, 3-or-better, the bar
and the best month — and keeps average, behind, played and misses; the
season under it is a grid of tiles, a trophy for a month won, a lighter
tile for a podium. The rule line under the title says a missed day counts
as 7 only when the board is set to count it that way.

**The grid's columns are the standings** for the window it shows, each
heading carrying the player's average. It was name order, with the standings
in a rail beside it, so a column stayed put when the range changed; the
design folds the rail into the header, and a column now moves with the
range. That is the design's call and the cost is written down here. The
legend names the tones, a day not played, and the asterisk.

**Privacy** is a list of five questions, each led by a glyph — what is
stored, why, who sees it, cookies, how to leave — in one card under the
page head.

Where the build departs from the mockups, and why:

- **Settings stays in the account menu**, as before.
- **About counts days, not players.** The mockup's "N players · N days"
  would tell a share-link guest how big the group is.
- **About's facts say what the app does**, not the mockup's placeholder
  wording where that would be inaccurate.
- **The lead-day strip is not clickable**, above.
- **Today keeps its overall rank in parentheses and the form note**, which
  the mockup's rows drop; the callouts stay.

## The third pass: the leaderboard, the Puzzle page, and the cards around them

The design's third pass redrew the leaderboard, added a page per puzzle, and
filled out Months and a player's page. What was decided on the way:

**The leaderboard is all time by default, with a 90-day switch.** The range,
the ranking rules and the sort are all links, so none of it needs a script.
Each rank carries an arrow for how it moved against **the same board a week
ago, rolling** — the history up to seven puzzles back, over the same range
ending then — rather than since the month began, which would reset on the
first and read as nothing for a week. Each name carries **the gap to the
player above**, measured on the averages as printed, so the line never
disagrees with the two figures beside it; the leader's says by how much it
leads.

**The board's streak is days played, a failure included.** It was days
solved, which a failure broke. The design's note says what the column means
— "days in a row with a result — a fail still counts" — and a board about
turning up every day reads better that way; the solving kind is not lost, it
is "Longest solve streak" in a player's Records beside "Longest play
streak". Both are computed from the unfiltered history, as streaks always
have been, and the board shows the whole history's streak even on the
90-day range, since a range that cut a streak short would report a streak
nobody has.

**Head to head is two presses and a query parameter.** The ⇄ on a row adds
that player to `?cmp=`; a second makes the card, a third drops the older
pick. The card counts the days both played in the range: who needed fewer
guesses, a miss as 7, how many were level, and by how much the one ahead
was ahead on average.

**Two columns went with the design**: the last game, which the last five
already show, and the games in the form window, which "Puzzles" replaced
with the games in the range. A lapsed player's row says when they last
played on the line under the name, where a ranked row has its gap.

**One row markup, two layouts.** On a phone the design stacks a row — the
rank over its arrow, the name over the gap over the last five, the average
over the form — and the same markup re-flows into that with grid areas
rather than a second copy of every row. A long name holds its line and
shortens last; the ⇄ stays put. On a phone the head-to-head card gives each
player a line of their own, since two long names and a score wrapped the
score away from its name.

**Every puzzle has a page, `/puzzle/{no}`.** It is not a view in the bar: it
is reached from a day — Today's results, a date on the grid, a day on a
player's page — and from the day either side of it. It lists everyone who
played, best first, with the squares they posted, and says how the group did
and how hard the day was among the last 90. A day from before grids were
kept says so and shows the scores alone.

**The grid defaults to 30 days**, as the design has it; it was 90. Pressing a
name picks out that column and dims the rest — radio buttons and a
stylesheet, no script: one hidden radio per column plus "none", each head a
label for its own radio or, while picked, for "none". A stylesheet cannot
count columns, so the rule tying each radio to its column is written per
column by the template. `TestBrowserTheGridPicksOutAColumn` pins it, because
the first version dimmed the picked column too — an id in the generic rule
out-ranked the per-column ones — and nothing that reads the markup could see
that.

**A player's page** gains Records and rivals (best month, both streaks, ones
and twos in the last year, and the two players they fare worst and best
against over the days both played), the day of the week against the group's,
and a heatmap of the last year — the last 26 weeks on a phone — whose days,
like the last thirty's, open a popup with the day's puzzle a press away. The
weekday averages are withheld below the ranking threshold like every other
average. The streak left the head for Records, which leaves three figures.

**Months** gains four awards under its headline — most improved on the month
before, fewest fails, most ones and twos, the longest solving run — among
the players it ranks; a title shared by more than two goes to "N players".
The season is the selected month's year, with a switch between years that
appears only when there is more than one.

Where the build departs from the mockups:

- **The Puzzle page numbers everyone who played**, as the mockup does, while
  Today still numbers only the players the board ranks.
- **A miss says "missed"** on the Puzzle page rather than a distance from
  the player's average, as on Today: a miss is off the scale the average is
  on.
- **The leaderboard keeps its unranked players** under a divider, with the
  reason and when they last played; the mockup shows only the ranked.
- **The leaderboard's columns still sort** from their heads, drawn as the
  mockup's plain labels.
- **Ordinals** ("3rd hardest") are spelled per language in code, English and
  Swedish only.

## Settings and the admin area, fifth and sixth passes

The design's fifth and sixth passes redrew a reader's Settings and the whole
admin area, and added a toast and a fuller search. What was decided on the
way:

**The glass follows Apple's Liquid Glass, in two strengths.** The bar's
pieces are the clear one: a thin neutral fill, the page behind saturated
rather than tinted, a hairline with light caught just inside it and a soft
shadow — matched against screenshots of Safari's toolbar in both themes. The first cut was the design's Frosted — a warm, heavier fill with a
flat hairline — and read as a tinted panel beside Safari's own toolbar on
the same screen. The menus, the toast, the search overlay and the sign-in
card stay frosted (`--glass-menu-*`) because they carry text. Reduced
transparency makes the glass solid; increased contrast trades the rim for a
firm border.

There is no scroll edge of our own — no band across the window behind the
bar to soften what scrolls under it. One was tried, and on an iPhone the
area under the clock turned solid rather than showing the page run up
under it, even with the band starting below the status bar. Nothing else
was fixed across the width of the window there, so the band was the
cause; the likeliest mechanism, not confirmed, is Safari taking a
full-width fixed layer at the top for a page header and filling the
status bar with it. Safari draws its own soft edge under the status bar
when nothing claims it, which is the look wanted anyway. Nothing fixed
spans the top of the window, and
`TestBrowserNothingFixedSpansTheTopOfTheWindow` holds it there.

**Settings is three cards — profile, sign-in, two-step — and every risky act
asks first, in place.** Generating new recovery codes, rotating the secret
and turning two-step off each open from a danger-outlined control into a
danger box inside the card (`?confirm=codes|rotate|totp`), which commits in
red or cancels. Setting up or rotating is drawn in the same card
(`?setup=totp`), so the enrolment dialog is gone; the password is still asked
for, for the reasons under *Authentication*. Fresh codes are shown once, with
a download that is a `data:` link — no script — and a copy button that
needs one and is hidden where no clipboard can be written.

**A toast says what just happened, and closing it is a link.** It floats in
frosted glass over the foot of a wide window and, on a phone, under the bar:
the foot of a phone is the browser's (see *The navigation floats*). It
has **Undo only where the earlier state is an address**: the leaderboard's
"Back to default". That link carries the query it changed from
(`?changed=reset&undo=?mode=hard`), and only a query string is ever followed
from it. The admin area's toasts have none, for the reason under
*Deliberately not built*: undoing a discard or an assignment is not a return
to an address but a second act.

The design also had a toast after each single rule in the ranking menu and
after the range, and they were dropped: the board arrives with the menu
still open, so the menu itself shows what changed and the row just pressed
is the way back, and the range is a segmented pair. The reset is different
— it can change several rules at once and takes its own button with it.

**The admin area has a home, on a phone.** `/admin` is the design's list of
the five sections, each with a line of state (who is waiting, how many can
sign in, the last change, the bridge). On a wide window the sections are a
segmented bar under the page head instead and `/admin` is rarely seen. The
account menu's admin row goes to Pending while anyone waits and to Settings
otherwise, and the avatar carries the count of senders waiting everywhere but
Pending itself.

**A player's editor holds their Signal senders and their login.** A sender
can be unlinked; a login reset, unlinked, attached from the list of logins
with no player, or switched off. The design's "Delete user" is **switching
the login off and unlinking it**: the activity log refers to users by id, so
deleting one would break the log, and switching off is what an admin means.
It is never offered on an admin's login. An invitation still waiting can be
resent or cancelled.

**Add player** (seventh pass) opens the design's New player card in the
editor's place: a name, an address, and any senders waiting in Pending to
take onto the board as this player. Creating the player and claiming the
senders is one transaction, so a claim that fails leaves no player behind.
The design fills the address in as the name is typed; here it is left
empty and the server makes it from the name, since filling it live would
take a script for a convenience. A clash is refused naming who has the
address, as the design does. The senders are checkboxes rather than the
design's one choice: a player can post from more than one account.

**Pending is a card per sender**, not a row per result: who they post as,
their latest tile, how many more are held, a suggestion when their name
matches a player's, and a native select — which may name **a new player**
from the name they post as, created on assignment.

**Search finds players, pages and puzzles.** A player's row leads with their
tile today and ends in their rank and average; a page with its own glyph; a
number, with or without `#`, finds that puzzle and says how long ago it was.
An empty query shows the first three of each. The overlay's clear is a
`<button type="reset">`, so it needs no script; its placeholder is the long
one on a wide window and "Search" on a phone, picked by the script that
opens it, since the overlay exists only with one. The bar's own label is
unchanged.

**The bot's language model is the Signal card's.** `LLM_URL` and
`LLM_MODEL`, from the replies work on main, shape what the bridge answers,
so the admin Settings screen groups them with `SIGNAL_*`.

Where the build departs from the mockups:

- **No Undo on the admin area's toasts**, above.
- **No toast after a single ranking rule or the range**, above.
- **"Delete user" switches the login off**, above.
- **The invitation keeps its language select**: the invitation, and the
  account it starts, are in the recipient's language, not the admin's.
- **Rotating the secret stays danger-toned**, as the design now draws it too.

## The sweep before merging

The last pass rendered the design's own mockup in a browser beside the app,
page by page at both widths, and measured the differences. What was changed,
and what was kept on purpose:

- **The page is the design's 1160px of content** with 60px margins at a
  1280px window, not 1080 with 100: the wide tables were where the room was
  missing. On a phone the page's 16px is the only margin; an old rule from
  the rail's days padded `<main>` by 12px more.
- **The leaderboard** takes the design's 34px head row, its Ranking button
  (a bordered rectangle on the card colour, the icon green while the
  ranking is custom), and its 82px phone row. On a phone the "Ranking:"
  prefix goes, so the range and the button share one line in both
  languages. The space after ▼ stays: it reads better than the design's
  "▼0.03".
- **Head controls stay on one line on a phone** where they can — a label
  drops before a control wraps (Ranking:, the grid's "Inactive") — and wrap
  rather than hide one when they cannot.
- **Months** on a phone draws the four awards as rows of one card, as the
  design's later pass does. The season's line under a name is two figures,
  times in the top three and the average place; the running month's
  "Partial month" line and "failed" (not "fails") stay.
- **The grid** keeps its own scroll box, writes puzzles as "#1926" and days
  as "27 Sep", and gives each column a floor so tiles never touch; past
  that the grid scrolls sideways. Its phone layout is due a redesign.
- **A player's** trait chip stands beside the name. The retired mark that
  stood with it is gone from the head: two chips fought for the line, and
  the roster and the board already say who has left. The eyebrow says when
  they last played as the design does — today, yesterday, 27 Sep — so it
  stays one line.
- **An award's holders are links** to their pages; a count ("3 players")
  links nowhere.
- **A link that stays on the page keeps the scroll.** htmx scrolls every
  boosted swap to the top, which is right for a step to another page and
  wrong for a change to this one — another player in the roster, the
  board's range, a ranking rule or a pair to compare, a month from the
  season, the grid's window, closing a toast, a confirm in Settings. So
  `app.js` compares the page a swap leaves with the one it lands on — the
  same path, or the same kind of page under it (`/players/…`,
  `/puzzle/…`, `/admin/players/…`) — and when they match, swaps with
  `show:none`. No link says so itself, so a new control gets it for free.
  It is still the whole body that is swapped, never a reload; the bar
  holds still as it always has. A step to another page, a player's name in
  the season included, starts at the top as before.
- **An unranked player on Today** has a dash in the average column, like
  every other figure withheld from an unranked player, and the dash opens
  why — how many puzzles they have and where the board starts ranking. The
  count it replaced wrapped to two lines.
- **Focus placed after a swap shows its ring only for the keyboard.** The
  script puts focus on the pill, tab or ranking row for the page that
  arrived, so the next Tab moves on from there; after a pointer press it
  does so quietly, since Safari draws the ring for any focus a script sets
  and a clicked pill was left circled.
- **The avatar's badge** is the design's 22px at both widths. It counts
  senders waiting for now and is named for where it sits, not for
  pending, since other notifications may use it.
- **Kept as they are:** Today's green puzzle number in the eyebrow, the
  Puzzle page's row height and its full month name.

## Switching pages in place, and what that says about the script rule

AGENTS.md's rule is that script is for what cannot exist without it. This
work roughly tripled `app.js`, and that is worth explaining, because it is
not a change of rule.

Nothing the script adds is a feature. Every link is still a link to a real
URL, every menu a `<details>`, every form a form that posts, and each works
with `app.js` absent, disabled or failing to load — which is also how each
enhancement is written: it takes a press the markup had already handled, and
saves the round trip. What is added is never the thing, only the absence of a
reload.

The page switcher is the general case. Following a link cost a full page
load: the document torn down and drawn again, with the bar, the rail and the
wordmark — identical on every page — going white and coming back. On a phone
that reads as the application blinking each time it is touched.

Two decisions inside it are the ones worth keeping.

**The whole body is replaced, not just the content.** The rail's highlight,
the theme and language links — each of which is the current URL with one
parameter changed — and the title all belong to the page being moved to.
Patching the handful known to differ is a list that goes stale the first time
somebody adds a control to the bar. Replacing the body means what arrives is
the server's own rendering of that URL, so a page reached by script and a
page reached by following the link are the same page. It is also the only
version that gets an error page right: that frame has no rail, and replacing
only the content would have left one behind.

**One mechanism, not one per route.** Two pages used to answer `?partial=1`
with their card alone, so that the board's ranking menu and the player roster
could each swap in place. Both are the general case now, and the parameter is
search's alone — the one place it still means something, because the ⌘K
overlay wants a list of hits rather than a page.

What it costs is a constraint on every enhancement written from here. An
enhancement that delegates from the document is unaffected; one that holds on
to a particular element is holding a node that a swap throws away. The ones
that do — search, the copy buttons, and (until the fifth pass took it out) the
raised outcome — register with a
re-init registry that runs them again after every body swap, and so must be
safe to run more than once.

The switcher itself was hand-written, three hundred lines of it, and is
htmx's now — see *htmx does the fetching*, below. What it asserted by hand is
asserted by the browser suite: no reload, Back restores the page and its
scroll, a switched-in page starts at the top, focus lands somewhere a reader
can use. See *What only a browser can check*.

## htmx does the fetching

Every enhancement above but three had the same shape: take a press the
markup had already handled, fetch the URL it pointed at, put what came back
in place of something, reconcile. The page switcher did it for the body, the
share slug's rotation for a card, the palette for a list; the rail's
collapse did the visible half first and fetched afterwards. Each was written
by hand, and together they were most of `app.js`.

htmx is that shape as a library: an attribute says what to fetch and where
to put it, and the file does the rest — history, scroll, the request that a
newer request should cancel. So the body is boosted (`<body hx-boost="true">`,
in `base.html`), which is the page switcher, and the two places that swap
something other than the body say so in their own template. `app.js` keeps
what htmx has no attribute for.

**This goes against two recorded decisions, deliberately.** AGENTS.md's
"vanilla, no dependency", and *No new dependency, and the rule stands* below,
which turned down a small websocket client for the tests. The rule was that
a dependency needs a reason, and the reason here is the one above: the file
had grown into a bespoke copy of a well-known library, and every new
enhancement was going to add to the copy. What the rule was for still holds
— no npm, no build step, no bundler: htmx is one file copied into `static/`
and embedded like the stylesheet, and upgrading it is copying a newer one.
Version 2.0.10, 0BSD. Version 4 exists and is a rewrite with a different
extension model; the SSE extension this work also needs targets 2.x, so 2
it is.

What did not fit, because the shape is worth knowing where it ends:

- **A dialog is not a swap target.** *(Moot since the fifth pass: the
  enrolment is inline on the settings page and there is no dialog.)* The
  enrolment dialog fetches a card
  that is a template shared with a standalone page. htmx wants the dialog's
  target on a container the card lands in, and every link inside — whose
  job on the standalone page is to navigate — would then have needed
  attributes undoing the container's; the form's Cancel sits inside the
  form. So the dialog keeps its own requests, and is never handed to htmx at
  all: nothing inside it is boosted, and every press in it is `app.js`'s.
- **htmx boosts what it has processed.** Everything the server renders and
  everything htmx swaps in is; a node a script inserts is not, until
  `htmx.process` is called on it. The palette learnt this the first time a
  hit reloaded the page it led to.
- **htmx's listener is on the element**, so it fires before anything
  `app.js` delegates from the document. A control `app.js` takes over — the
  search button, the enrolment link — says `hx-boost="false"` where it is
  rendered, or htmx fetches the page first. This replaces the old
  constraint that the switcher's listener be registered last.
- **htmx processes swapped content after a settle delay** of 20ms, and a
  link in a fragment is not boosted until then. The delay exists for CSS
  transitions on swapped content, and it is zero here even now that a swap
  is animated — see *A switch looks like one*, below — because the
  animation is the browser's view transition, which htmx holds open with
  its own promise and resolves at the end of the settle callback. A delay
  would only postpone the transition, not smooth it.
- **htmx forgets a request was boosted if its element leaves the page.**
  Whether a boosted swap pushes the address is read off the element once
  the reply is in, and a swap that removed the element in the meantime — a
  search hit pressed as the results refresh, a player pressed as a live
  update lands — has wiped it. The page changes and the address does not.
  `app.js` makes the decision at request time instead, and pushes wherever
  the server ended up. Seen in one run in four before it was understood.
- **Forms are boosted too.** The old switcher took links only. Opting every
  form out would have been fighting the tool for a distinction a reader
  cannot see, and the no-script path is a form that posts, as before.
- **The rail's collapse lost its instant half, and got it back.** (The
  rail has since gone; the theme is the one attribute still set ahead.) The
  hand-written version flipped the width on `<html>` before asking the
  server; the htmx move made it one boosted link among the rest, with the
  width arriving on the page, on the theory that on a local network that
  is the same moment. It is the same moment for the *latency*. It is not
  for the *motion*: the rail's width transition runs on the node the swap
  is about to throw away, and the node that replaces it arrives already
  at its new width, where a transition never runs. The rail jumped, and
  pressing it read as a reload. `app.js` sets the attribute at the press
  again — but generically, off the `?sidebar=` or `?theme=` parameter the
  link already carries, since `urlWith` is the one place that builds these
  links and the parameter is the encoding. It knows a parameter, not a
  control, which is why `chrome_test.go`'s assertion that the script never
  mentions the collapse control still holds. The reply confirms it: a page
  that arrives overwrites the attribute as before, and a request that ends
  with no page puts back what was there. `?lang=` is deliberately not
  flipped ahead: an `<html lang>` claiming a language its words are not
  yet in misleads exactly the reader that consults it, and nothing is
  faster for it.
- **Back shows the page as it was left.** htmx keeps the last ten pages in
  the tab's `sessionStorage` and restores one on Back with its scroll
  position, which is what the browser does for a page it loaded itself. A
  result that landed in between is not in the snapshot; a page that
  subscribes to live results asks for what it missed the moment it is back
  on screen.

### A switch looks like one

*Superseded in part by the glass redesign: nothing is named any more, and
the whole window cross-fades — see* The navigation floats. *What follows is
why the fade exists at all, which still holds.*

Replacing the whole body was the right call, and it had a cost the first
version did not pay for: a hard cut of everything on screen, which is what a
reload looks like. Nothing reloaded, and it read as if it had. Every swap is
now a view transition — `globalViewTransitions` in the htmx config, which
also covers Back and Forward, whose swaps have no attributes to carry a
setting — and three `view-transition-name`s in `app.css` decide what it
looks like: the bar and the rail, the same on every page, are named and
hold still; the content is named on its own, so the cross-fade is the
content's and not the whole window's. The names are `topbar`, `rail` and
`content`, and a name must be unique on each side of a swap or the browser
abandons the transition with a console warning — which the browser suite
listens for.

- **The rail's name is what finishes the instant half.** Pressed, the
  rail starts its own width transition on the node the swap is about to
  replace; the view transition captures that node wherever the CSS got
  to, and morphs the rest. Whether the reply lands in one frame or in
  eighty milliseconds, there is no moment at which the width jumps.
  `hx-preserve` on the rail would have done the same and frozen the
  current-page marker on the old rows; it was not used.
- **Two swaps are not a navigation and opt out** on their own `hx-swap`.
  The live region's redraw: a result landing is not something the reader
  pressed, and a cross-fade that pauses the page for it is motion for an
  event they did not cause. The search overlay's results: `hx-trigger` on
  the input fires on every debounced keystroke, and a document-wide
  transition on each would freeze the page as you type. (The admin
  settings card had a swap of its own, left transitioning; the fifth pass
  made rotating the slug an ordinary post and redirect, with a toast.)
- **A press in the board's ranking menu does not fade either**, cancelled
  in `app.js` since the press is an ordinary boosted link. The menu is open
  on both sides of that swap, and Safari draws the cross-fade from pictures
  of the page without the frosted glass: the open panel went clear for the
  length of the fade and frosted again after, which read as a reload. Not
  seen in Chromium, which keeps the frost in its pictures.
- **`content` is named only inside the shell.** The sign-in and error
  frames render a `<main>` too, and naming it there would morph the
  page well into the sign-in card on sign-out. Those frames cross-fade
  as a whole, which is right for them.
- **Reduced motion cancels the transition, not just the animation.**
  `app.js` prevents `htmx:beforeTransition` when the reader's system asks
  for reduced motion, so `startViewTransition` is never called and the
  browser never pauses to take its pictures; the stylesheet zeroes the
  animation as well, for a transition started any other way. Read at each
  swap, so a setting changed mid-session is honoured.
- **Navigating to the error frame is an exit transition.** The three
  named elements exist in the old capture and not the new; the browser
  fades them out. Rare, correct, and not special-cased.
- **The duration is 160ms** (`--transition-page`), down from the browser's
  250ms. Rendering is paused for the whole of a transition, so longer is
  not smoother past the point where a change reads as motion.

### When this stops being enhancement

`app.js`'s "What htmx leaves to this file" block is the one to watch. It
holds the `<html>`-attribute sync the body-swap design forces, and one
workaround for an htmx 2.0.10 quirk (the push-URL flag). That is the shape
of code that grows into fighting its library. The additions above are
declarative — a config flag, three names, two opt-outs, a listener that
cancels — and none is a new mechanism. What would be:

- a second htmx-version-specific workaround in `app.js`;
- an enhancement that has to re-implement something the server renders
  (the enrolment dialog was the one island of that, and has gone);
- state on `<html>` that is not also a cookie the server reads;
- an htmx upgrade — 4.x is a rewrite, and the SSE extension pins this to 2.x.

Any of those is a reason to reopen the architecture rather than add to
the block.

## Static files are cached by their content

`serveStatic`'s comment promised a long cache lifetime keyed by build, and
for a long time the file server underneath sent every file in full on every
page load: an embedded file has no modification time, so there was no
`Last-Modified`, no `ETag` and no `Cache-Control` at all — the stylesheet,
three scripts and the font, each time a page was opened.

Each file now carries an `ETag` that is its own digest, and every page
links each file with `?v=` set to that digest through the `asset`
template function. A request naming the current digest is answered as
immutable for a year; the URL changes when the file does. Any other request
gets an hour, so a page cached with an older `?v=` cannot pin an older file
forever. The font is immutable on its path alone, because `app.css` names
it by a literal URL and the convention there is to rename a font file
rather than change one in place.

Per-file digests rather than `version.Commit`: the commit is empty for any
build made outside CI, and a developer's build has to cache the same way.
Per file rather than one digest for the build, so a change to the script
does not throw away the cached font.

## A result that lands appears on the pages that are open

Today and the leaderboard redraw themselves when a result is filed, over a
server-sent event stream and htmx's SSE extension. Four decisions inside it
are the ones worth keeping.

**The stream carries a mark, not HTML.** The obvious design — the server
knows when a result lands, so it renders the fragment and pushes it — ran
into what a fragment depends on: the reader's language, whether they are on
the share prefix, the hard-mode filter in their URL, and a chrome that
needs the request to issue a token and set cookies. Rendering it in a
goroutine would have been a second template layer, or one render per open
page per event anyway, minus the request. So the stream says only that
something changed — the id of the last activity-log row that filed a result
— and each page fetches its own URL again and swaps its content in place.
One render path; the fragment is never wrong for the reader looking at it.
The cost is a handful of small GETs per event, for a group of a dozen.

**The server learns by polling, not from the bridge.** Results land from
the bridge, from `/api/ingest`, from a claim on the pending screen and from
the CLI in another process, and a hook in the bridge would have caught one
of the four. Every path writes the same activity-log row in the
transaction that writes the result, so one indexed query every two seconds
— while anyone is listening, and not otherwise — sees all of them. The
brief said "the server already holds the Signal websocket, so it knows";
it does, and it is the wrong thing to know from.

**A page carries the mark it was rendered at.** The stream is opened with
`?since=<mark>`, and a stream that opens behind is caught up by one event
at once. That is what makes a page restored from htmx's history cache, or
a reconnect the extension makes from scratch, show what landed while it
was away; a browser reconnecting a dropped stream sends `Last-Event-ID`,
which is honoured the same way. Nothing is queued server-side, because
nothing needs to be: the only fact a stream can miss is "something
changed", and the mark says whether it did.

**Two pages subscribe, and the stream is behind the same door as they
are.** Today and the board are what people leave open; Months, the grid,
the roster and a player's page are archives, and each subscription is a
connection held open. `/events` sits behind `requireAuth`; the share view
has its own copy under the slug, whose constant-time check is the whole of
its authentication, as for every shared page — and what a stream carries is
a number. Sixty-four streams at once is the ceiling, well above a group
with a tab each; the sixty-fifth is told to come back. `Server.Close` ends
every stream and is registered with the HTTP server's shutdown, because a
stream is never idle and `Shutdown` would otherwise sit out its whole grace
period on every restart with a tab open somewhere — the one line this work
adds outside the view layer.

Two things the browser taught, worth knowing before touching the region:

- **Attributes on the region are inherited by every boosted link in it.**
  `hx-select`, `hx-target`, `hx-push-url` on `<main>` reached the player
  links, which then swapped the player's page into the region at the same
  address. `hx-disinherit` on the region names what is its own.
- **A page navigated away from keeps its stream open in the browser's
  back-forward cache.** One more open stream per visit to Today, and at six
  Chrome has no connection left for this host and the next visit never
  loads. So `app.js` closes every stream on `pagehide`, and reloads a page
  the cache brings back — it is stale and has no stream, and the old
  switcher reloaded for an entry older than anything it drew for the same
  reason.

What it deliberately does not do: keep an open `<details>` open through a
redraw. A rank popup or the "still to submit" list open when a result lands
closes with the content it was part of. A result lands a handful of times a
day; the fix — `hx-preserve` on each — is one attribute per disclosure and
can come when somebody minds.

A later single-page island on one route — a stats explorer has been talked
about — inherits the boost and the history cache. Its root says
`hx-boost="false"` or its own links are hijacked; it mounts again on
`htmx:load` and `htmx:historyRestore` or a restored snapshot of it is dead
markup; and its bundle goes in `<head>`, because scripts inside a swapped
body are not run.

## Every control is one of four things

A screen's controls had drifted into six treatments that meant nothing in
particular. A filled green anchor and a filled green button were 32px and 36px
and stood side by side in one row. `Cancel` was an underlined green link in
the player editor and an outlined button in the slug rotation's question.
`Discard` — a delete — wore `.link.danger` and came out accent green, because
`.link.danger` is only red inside the account menu, where a different rule
happens to catch it. `Assign`, the main thing a pending row is for, was a grey
outline. And the control that rotates a two-factor secret was filled green —
the tone this app uses for "safe, and the ordinary thing to do here" — on a
press that silences every authenticator app holding the old secret and cancels
the recovery codes.

None of that was a decision. Each was locally reasonable and nothing held them
against each other, which is how six treatments happen.

There are two axes now and nothing outside them. **Weight** says how much of
its group a control should take: `.btn` is filled, for the one thing the group
is for, and `.btn.secondary` is outlined, for a real control that is not that
one. **Tone** says what pressing it costs: the accent is safe, `.danger` means
something stops working or is deleted. Four combinations, one meaning each,
and they are written down in `internal/web/templates/README.md` under
*Controls*.

Two consequences worth stating, because they are what the system buys:

**An act that cannot be undone reads the same wherever it appears.** An
outlined red control opens the question; the filled red one inside the
question commits it — filled there because inside that block, committing is
what the group is for. Rotating the share slug, rotating a two-factor secret
and turning two-factor off are now all that shape, and a reader who has met
one has met all three.

**Tone follows the consequence, not the screen.** The same enrolment control
is `.btn` when setting a first secret up and `.btn.secondary.danger` when
replacing one, because only the second costs anything; replacing now asks
first, like the other two, and the setup it leads to commits in red. A control that is green on one visit and red on the
next is telling the reader something true.

What is deliberately outside the system: `.link` is a prose link and never
goes on a `<button>` — a control that looks like prose is a control nobody can
find. Navigation and selection — nav rows, menu rows, settings tabs, chips,
badges, the grid's filter toggle — are not controls in this sense and keep
their own rules; the account menu's rows in particular are drawn by being in
that menu, whatever element they are.

The two things most likely to rot are held by tests rather than by care:
`TestEveryControlIsOneOfTheFour` refuses a `<button>` with no control class or
with `.link` on it, and `TestADestructiveActAsksBeforeItActs` pins the
open-then-commit pair on the acts that have one. The search overlay's clear
and esc/Cancel are the design's own and outside the four, like About's close.

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
- **Admin UI for tokens and result corrections.** The share slug has had its
  rotation on the admin Settings screen for a while, and since the fifth pass
  a player's editor resets, unlinks, attaches and switches off their login.
  Creating a user outright is still the CLI's, which remains the fallback and
  the bootstrap path regardless — it is the only thing that works before a
  user exists.
- **Filtering the activity log by player.** It filters by kind, and a row
  opens to what was recorded.
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

## Timestamps are stored in UTC and shown on the server's clock

**Every timestamp is stored in UTC, as `2026-09-23 03:29:14`** — SQLite's
own form, the one `CURRENT_TIMESTAMP` writes. A time bound from Go used to
land as Go's own string instead, in the container's zone
(`2026-09-23 05:29:14.067 +0200 CEST`), so one column could hold two
spellings of two zones. Nothing went wrong in Go, where the driver parses
both to the same instant, but SQL compares timestamps as text, and a
comparison between the two forms is off by the offset: held results were
purged two hours early and an expired invitation stayed pending two hours
late. The connection now converts every bound time to UTC and writes the
same form (`_timezone=UTC`, `_time_format=datetime` in `store.dsn`), so a
new column or query cannot reintroduce it, and migration 0013 rewrote the
values stored the old way. The zone is not spelled in the value; it is the
same zone in every column, and this is where that is written down.

**The server's zone applies when a person reads a time, and not before.**
The database hands times back in UTC; the container's `TZ` decides what is
shown. Anything that asks which *day* something belongs to — which puzzle
is today, the recap's 00:01 run, month boundaries — also uses the server's
zone. A result's day is its puzzle number, never its posting time, so a
post at 23:30 counts for the puzzle it names whatever the clock says.
Ordering by time needs no zone at all: instants compare the same in any.

**How a time is shown:** in the CLI and on the admin pages as
`2026-09-23 05:29:14 +0200` (`i18n.Timestamp`), the offset rather than a
zone name because it is the part that compares against another clock
without looking anything up; everywhere else on the web as a local time
without an offset. The one exception outside admin is the password reset
email, which carries the offset: somebody who receives a reset they did not
ask for may want to know exactly when the request came in.

## Announcing the month

The bridge's first step from receive-only to bidirectional: posting the
month's winner back into the group when a month closes. Deliberately small
— one message, once a month, no significance threshold and no memory of
previous standings — because that is exactly what makes it safe to build
before the larger idea it is a step toward, announcing rank changes as they
happen. The day's recap has since taken the first of those steps: its 👑
line, below, says when a day handed the month's lead to somebody new, and
its 🐐 line when a day put somebody alone at the top of the all-time board.
The all-time line rides on the recap rather than being a message of its own:
the change can only happen when a day's results land, and that is the moment
the recap already posts.

**A month closes the way a day does: when every active player has filed its
last day, or at the run just after midnight.** It used to be noon on the
first, to give late closing-day posts the morning. That protected nothing
the day's recap did not already give up at 00:01, and it moved the month's
reveal to the middle of a working day, when the moment it lands best is the
evening the last result comes in and the group is in the chat. The
standing is no secret before then anyway: the board shows it. The schedule
uses the deployment's local timezone, the same basis as puzzle dates.

**It goes out after the recaps of its last day**, the day's and, when the
month ends on a Sunday, the week's — smallest first, so the group reads the
day and the week before the month they closed. `NewMonthly` waits for each
of those that is configured, and for the week only when it will be posted
at all, and only until the day after the last day: from then on the day's
recap can no longer come, and a failed send elsewhere must not hold the
month back for good. All three share the one run just after midnight.

**A later live result is the catch-up trigger**, alongside the run's check on
start and every later midnight. If the app was offline at midnight or the
send failed, the next accepted result checks whether the previous month
still needs announcing.
Ordinary conversation is not a trigger: if sending succeeded but recording
failed, receiving the bot's own announcement must not immediately send
another copy. Explicit Archive shares and older back-dated results are also
excluded, so replaying an old puzzle cannot make the bot speak.

**The catch-up check only ever looks at `previousMonth(now)`, deliberately,
not at every unannounced month back to some watermark.** The gap that leaves:
if every check through the following month fails — each midnight run and
every live result — the next month's checks only examine their own previous
month, and the missed one is never revisited. Accepted rather than fixed, because a
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
for their final conjunction, `list.and`, which is a word in each language —
`and`, `och`. English used a language-neutral `&` first; it read as a logo
rather than a sentence in a chat message, where "Alice & Bob shared the day's
best" is prose and not a label. One key for both surfaces, so the board and
Signal cannot disagree about it.

**Turning it off is a separate switch from configuring the bridge at all**,
`SIGNAL_ANNOUNCE_MONTHS`, defaulting on. The bridge's own on/off state
already answers "does this deployment talk to Signal"; this answers "does
the bot ever speak in the group," for someone who wants results flowing in
without the bridge ever posting back.

## The day's recap

The second thing the bot says in the group, and the first that happens more
than once a month: when a day is over, post what the day was and where the
month stands. It reuses the month announcement's whole shape — a closure of
the same signature, a row written only after a successful send, a scheduled
run plus a live-result catch-up — so what follows is only where the two
differ.

**A day ends when every active player has filed, or just after midnight,
whichever comes first.** The month deliberately does *not* wait for
everybody (see above: one missing player must not block the group
indefinitely), and the day deliberately does, because the two are asking
different questions. A month is a standing that a late result barely moves;
a day is a race, and posting "Alice took it in 3" while three people have
not played yet is not a result, it is an interim score. Midnight is what
stops that from blocking anything: the day is over by the calendar whether
or not everyone turned up.

**Retired players never hold the day open.** They are not expected, so
counting them as missing would mean the early trigger silently stopped
working the first time anybody left the group — the kind of fault that looks
like nothing at all. This falls out of reusing `stats.ComputeToday`, whose
`Missing` already excludes them for the same reason on the Today page.

**The scheduled run is 00:01, not 00:00.** A result posted at 23:59 still
has to reach signal-cli, cross the websocket and be filed, and a recap that
lands a minute late is worth more than one that omits the last score of the
day. It also keeps the run off the boundary itself, where a timer firing
fractionally early would compute the closing day as the current one and find
nothing to close.

**The day that has just closed is considered before the day in progress.**
One check posts at most one day, and it prefers the older: a closed day can
no longer change, and taking it first is what lets a live result catch up a
midnight run the app was down for. In steady state the closed day is already
recorded by the time anyone plays, so this costs a lookup and the early
trigger fires as normal.

**Only ever one day back**, for the reason the monthly catch-up only looks
at `previousMonth(now)`, and with a sharper version of the same gap: an app
offline for a week comes back and posts one recap, for yesterday. The days
before it are never announced. That is the intended trade — the alternative
is a burst of recaps arriving together, which reads as a malfunction — but
it is a real hole, and worth knowing about before concluding the feature
"missed a day".

**The daily schedule checks once on start, before its first sleep.** Without
it, an app down at 00:01 and up at 08:00 schedules nothing until 00:01
tomorrow, and the missed recap waits on somebody filing a result to be
noticed at all. On a day nobody plays, that never happens, and the missed day
then falls outside the one-day-back window and is lost — a restart turning a
late recap into no recap. The week's and the month's checks ride on the
same run and so check on start too.

**A first run marks the day before startup as done rather than announcing
it** — `SkipDailyBacklog`, called once before the scheduler's first check. The
days already in the database when this ships are history, not a missed post:
the group has moved on from them, and opening with a recap of yesterday is a
strange first thing for a bot to say. The check on start would otherwise do
exactly that, and deploying at midday would greet the group with the previous
day's result.

It writes one row rather than backfilling the whole history because the daily
check only ever looks one day back — marking yesterday is enough to make
everything before it invisible. The condition is "no day has *ever* been
announced", which is true for a fresh install and for an existing one
upgrading to this, and false forever after, so a later restart cannot move the
marker forward and swallow a day that genuinely was missed.

Note what this does *not* suppress: the day in progress. Deploy at 12:18 with
every active player already in and the recap for that day goes out at 12:18,
which is the intended first word — about the puzzle being played now.

**Absentees are counted, not named.** "4 of 6 posted" is the fact; a list of
who did not play reads as the bot calling people out, and this message is
pushed rather than requested. The Today page names them, because somebody
asking for that page is asking.

**Failures are named.** This looks like the opposite call, and the line
between the two is deliberate: a failure is a result the player posted in
the group themselves, in front of everyone, and the recap repeating it is
the group's own banter. An absence is not a result and was not posted by
anybody, so naming it is the bot's initiative alone.

**The recap is usually three or four lines.** The head, the day's best and
the month's standing as before; between them, who opened and closed the
day, then the day's events, then one line of colour. The events — a change
of leader, a streak on a milestone, somebody's first ever 2, a run at 3 or
better up to or past the group's record — are each told whenever they
happen, one line per kind: each is rare, and a day with two of them is a
day the group would want both. The colour — an unusually hard or easy
puzzle, who failed it, or somebody well under their own average — is
capped at one a day, the first of those that is true, and none on a day
none is: these happen often, failures most days in a group of seven, and a
remark that appears every day is wallpaper. The colour's order puts what
explains the day ahead of what merely happened on it. The thresholds live
beside the code that uses them. Everything but the standing is computed
from results up to and including the recapped puzzle, since at 00:01 the
next day's first result may already be in.

**The day's best is counted from four, and said as one when everyone got the
same.** Three names read; seven do not. And when every filer landed on the
same score the fact is about the puzzle, not about who tied.

**A first ever 2 is celebrated however short the history**, since a 2 is
rare enough that the second week is not too early — the only floor is one
earlier game, because "for the first time" on somebody's first day says
nothing. A first ever first-guess solve is the 🥇 line's own variant. A
run of threes was the same idea and was reframed before it shipped: three
3s in a row happens to somebody most weeks in a group of seven, so instead
the recap marks a run at 3 or better (a 2 extends it, a 4 or a day off
ends it) the day it draws level with the group's longest ever and the day
it passes it, then falls silent — a run that keeps going is on the board,
and would otherwise be this line every morning. The record it is measured
against leaves out the player's own open run, for the same reason, and has
a floor of three: in a young history the record is two and is beaten every
other day.

**Who posted first and last is read from `results.posted_at`**, the time a
result was posted in the group: Signal's server-received time, not the
bridge's receipt (a bridge catching up after an outage would otherwise call
whoever it happened to read first "first") and not the sender's device
clock (a phone set wrong would be first every day). It is the first known
posting and never moves: a re-post or a correction changes the score, not
when the player first posted — but a row first written by the API or the
CLI and then posted in the group did get posted, so an update fills an
empty `posted_at` rather than leaving it. NULL means not posted in the
group, or unknown: rows the bridge filed before results carried their
source stay NULL, since the activity log cannot vouch for them and a token
actor could have been a script. The backfill covers everything since the
bridge began writing as the application itself. A day with fewer than two
posting times has no order and gets no ⏰ line; a result filed by hand is
never "last" merely because it was entered late. The "as usual" and "N days
running" remarks come from `stats.ComputePostingHabits`, over the last
thirty days that had an order, and the thresholds are its constants.

**The month line reports the month the day belongs to, not the month the
clock is in.** These differ for exactly one recap a month — the 00:01 run on
the first — and reading it off `now` would print a brand-new, empty month
beside a day played in the old one. The rest of the month figures are still
computed as of the real `now`, so that recap correctly reports a month whose
days have all concluded.

**On that one recap, the month line is left out.** The 🏆 message follows
straight after it, and printing the standing would give the group the
month's winner, average and margin twice in a row, the second time with a
trophy. It used to point at the 🏆 message instead ("September wrapped — the
result at noon."), when that came twelve hours later; with the month posted
right behind the day, the pointer only delays what is about to arrive.

The condition is **the recapped day being its month's last**, not the month
having closed by `now`. Both ways a last day gets recapped give the result
away: the 00:01 run after it, and the early post on the evening of the day
itself — and the early one is not the safer of the two, because it only fires
once every active player is in, which is exactly when nobody is left to move
the figures. Testing "has the month closed" would have caught the first and
missed the second. Ordinary days inside the month are untouched; the standing
is a standing, and the group is told it every day.

**It is conditional on a monthly announcement actually being configured**,
which `NewDaily` takes as `monthResultFollows`. With `SIGNAL_ANNOUNCE_MONTHS`
off and `SIGNAL_ANNOUNCE_DAYS` on — a supported combination, since the two
switches are deliberately independent — nothing else would ever say where the
month finished, and leaving it out would mean nobody is ever told.

**The margin names every runner-up, not one of them.** `Month.Margin` is the
gap to the next *distinct* average, and several players can share it. Naming
one would invent a placing, the same reason `Winners` is a slice.

**Its own switch, `SIGNAL_ANNOUNCE_DAYS`, defaulting on.** Folding it into
`SIGNAL_ANNOUNCE_MONTHS` would have made that variable's name wrong, and the
two post at genuinely different rates: wanting the month's result without a
line in the group every day is a reasonable thing to want, and should not
require giving up both.

**The strings are their own key family, `announce.daily.*`.** Same reasoning
as `announce.line.*` against `months.line.*` — chat prose is not page prose.
One trap worth recording, because it fails quietly: `internal/web`'s
translator treats any key ending `.one` as the singular half of a plural
pair, so `announce.daily.best.one` was read as a plural form of
`announce.daily.best` and rendered with the count substituted into the name's
`%s`. A catalogue key must not end in `.one` unless it really is a plural.
`internal/web/i18n_test.go` catches this; the keys carry plain suffixes
instead — `announce.daily.best`, `.bestPair`, `.bestMany`, `.noneSolved`,
and for the opening line `.first`, `.first.usual`, `.first.run` and the
same three for `.last`: whole sentences per case rather than a sentence plus
a suffix, because where "as usual" sits differs between the languages.

**The day's best has three sentences, not a singular and a plural**, because
a pair takes a word of its own: "both" is wrong for three people and "all" is
wrong for two. Swedish restructures the sentence for a pair (`Både Alice
och Bob …`), which is exactly what a
separate key per case is for. Note also that this is not the catalogue's
`.one`/`.other` plural mechanism and could not be: that splits at one, and
this splits at two.

**A separate table, `signal_day_announcements`, keyed by puzzle number.**
Not a shared `announcements` table with a kind column: the two have
different natural keys, and sharing one would mean allowing the wrong half
of every row to be null. The puzzle number is the same identifier the
results table and the parser use, so there is no second notion of "which
day" to keep in step with `wordle.PuzzleForDate`.

## The week's recap

Once a week the bridge posts a recap of the Monday-to-Sunday week, in its own
message right after Sunday's daily one.

**It closes when Sunday does.** Sunday's last active player filing ends the
week early, the run just after midnight ends it otherwise. The week's check
rides on the daily run rather than a timer of its own, after the day's check
and before the month's,
and waits for Sunday's recap to be out when the day's recap is configured: the
two share a trigger, and the group should read about Sunday before the week it
closed. It stops waiting once Sunday's recap can no longer come — the day's
check looks one day back — so an outage costs at most the day, not both. Like
the day's, it looks one week back and no further, and on a first run marks the
previous week done rather than opening with it.

**Scored as a month is.** `stats.ComputeWeek` is the month's scoring over a
seven-puzzle span (`scoreSpan`, which the month now also goes through): a day
not played counts as 7, so the week cannot be won by playing only the good
days. The week is scored as of the following Monday even when posted on
Sunday evening — it is only posted early once every active player is in.

**Last place is named only among those who played at least five days.** With
missed days counted as 7, the bottom of the table is usually whoever was
away, and naming absentees is the one thing the recaps never do. So the 🥄
line takes the last of the regulars, and is left out when that player is in
the top three, which in a small group they can be. The same five days gate the
turnaround line, which also names people for where they finished.

**A core and at most three extras.** The core — head, winner, last place,
average — is there every week. Only the winner, not a podium: three names
and averages on one line ran together, and three lines of them were too
many for what second and third add. The extras each have a threshold and a
fixed priority (a run of weeks at the top, turnaround, rollercoaster or
metronome, full attendance, early bird), and the first three that fire go
out. Once a week can carry more than the day's three or four
lines, but not all of them: a message that needs scrolling is the one nobody
reads, and a varying set keeps it from being the same message every Sunday.

**Every line has to be able to stay away.** Three were tried and dropped
because they would have been there almost every week. Two more went once
seen in the group: the count of days' bests read oddly when shared bests
gave two players five of seven each, and the close finish between
neighbours was one line too many. The easiest and hardest day always exist, however flat the week was, and a day that really
stood out has already been called by the day's own 🧱/🪶 line. A tally of 2s
and Xs fires whenever there is one of either, which is most weeks. And a list
of who played every day names, by elimination, who did not, so attendance is
said only when it was everyone. For the same reason the metronome wants the
same score all seven days, not a week of 3s and 4s, which is just a week.

**A run of weeks is said from the second, its end from the third.** A shared
win counts for everyone sharing it, as a shared month does. Ending a run of
two is just a new winner, which the winner line already says.

**Keyed by the Monday's puzzle number**, in `signal_week_announcements`, for
the reasons the day's table gives. An ISO week number restarts every January;
the puzzle number does not, and a Monday is every seventh one from puzzle 2.

**Its own switch, `SIGNAL_ANNOUNCE_WEEKS`, defaulting on**, for the reason the
day has one.

## Answering questions in the group

The bridge's step from speaking on a schedule to speaking when spoken to:
a message that mentions the bot is a question, and the bot answers it.
`internal/reply` does the answering, with the same relationship to the
bridge that `internal/announce` has — the bridge hands over a sender and a
question and knows nothing about what is asked or how.

**A language model reads the question, and does nothing else.** The group
asks freely, in several languages, and a fixed vocabulary of key words was
tried on paper and found to break on the second way anybody phrases anything
— "vem är bäst?", "har Bo chans att komma ikapp?", "hur går det för mig
jämfört med förra månaden?". A model reads those fine. But the model only
ever produces a `reply.Request`: one of five kinds, a span, a number of days
and a player's name, constrained by a JSON schema the server enforces. The
figures come from `internal/stats`, the sentence from the i18n catalogues.
Small models are unreliable at arithmetic and ranking, and a leaderboard
that confidently names the wrong leader is worse than one that says it did
not understand. Under this split the model can misread a question; it can
never be wrong about a number.

**The model runs locally, as a third container.** The alternative was a
hosted API: faster, no memory on the box, and pennies a month at group-chat
volume. It was set aside because the question text is the group's
conversation, and sending it off the server is a decision the group did not
make when they let a bot into their chat. A 3B model on a CPU answers in
seconds and holds about 2.5 GB, which is the cost of keeping the
conversation on the box. The interface the model sits behind
(`reply.Interpreter`) is one method, so a hosted one is a small addition if
that decision is ever revisited. The container is the third in a deploy that
was deliberately two, and AGENTS.md says so now; the constraint that matters
— nothing beyond Docker and an `.env` file — still holds, because the app
pulls the model it needs on first start and nothing about the model is
configured by hand.

**A mention is the trigger, and only a mention.** Signal carries a mention
as the mentioned account's identity, not as text, so the bot's profile name
can change without the bridge caring, and nobody can trigger it by typing
the name. The account's own sent messages — which arrive as sync messages —
never count as mentioning it, so an answer cannot be read as a question and
answered again. A message that parses as a result is a result, whatever it
mentions: a score is never traded for a reply.

**Answers run beside the worker, one at a time.** The bridge files results
on one worker, and an answer takes the model seconds — up to a minute and
a half at the timeout. A score posted while the bot is thinking must not
wait for it: scores are what the bridge is for. So a question is handed to
its own goroutine, and the worker moves on. Answers still go one at a
time, since a CPU has one model's worth of attention, and at most three
questions are in hand — one being answered, two waiting. A fourth is
dropped with a log line rather than answered a minute later to a
conversation that has moved on; three at once is a group testing the bot,
not asking it. Shutdown waits for an answer in progress the way it waits
for queued results, on the same deadline.

**The bot shows it is reading.** An answer takes seconds on a CPU, and a
chat with no sign of life for seconds reads as a bot that did not hear —
people re-ask, and then get two answers. So a question gets a 👀 the
moment it is read, and the typing indicator until the answer is out; the
indicator is started again every ten seconds because Signal's clients drop
it after about fifteen. Both run beside the answer, never before it, and
neither can fail it: a slow or refusing signal-cli costs the sign, not the
reply. Only with replies on — there is no reading without an answer coming.

**A question is answered once or not at all.** The announcements retry on
the next live message because a month's result is owed; a question is not.
An answer that arrives after the conversation has moved on reads as the bot
talking to itself, so a failure gets one apology in the group and a warning
in the log. "The model is still loading" is an answer, not a failure, since
the first start of the stack pulls the model and a question during those
minutes is ordinary.

**The model is told names and a date, and nothing else.** `reply.Prompt` is
the whole of what it sees: the player names (which the board shows anyone),
the asker's name, today's date and the question. No results, no email
addresses, no account state, nothing from the users table. Combined with
the model producing only a `Request`, this is the guardrail: a question
about anything but the board cannot be answered wrongly because the answer
does not exist anywhere the model can reach, and the request it becomes is
"unknown": one short line back, and the question kept for the owner (see
below). `TestTheModelIsToldOnlyNamesAndTheDate`
pins the contract; a new field on `Prompt` is a new thing the model is
told, and that test is where it is decided.

**A reply to a bot post carries the post, and only a bot post — the
bot's own copy of it.** Signal sends a reply with the quoted message's id,
author and text, all written by the replying client: a claim, which a
modified client could make about any text. So the bridge keeps what it
sent, by the id signal-cli reports for each post, and a reply carries a
post only when its quoted id is one of those — and then the bridge's own
stored text, never the reply's. That is what makes "words this app wrote"
a property rather than a hope. With the post in the prompt as context, a
question under a recap — "what does this mean?", "what did Bo get that
day?" — can be read; the day the post is about is worked out from its
"Wordle <number>" here, since a puzzle number is a date by arithmetic and
the model would only guess. This is the one deliberate widening of what
the model is told. A reply to another member's message brings nothing
along, however it is phrased, because their words are theirs; nor does a
reply to a post from before the process started, which it no longer
remembers — the memory is in the process, the last few hundred posts,
because a table of the bot's own chatter is more than the case is worth.
The mention remains the trigger — a plain reply to the recap is
conversation, and answering it at all would make the bot a heckler.

**A question the bot could not place is kept for thirty days — the one
place the group's words are stored.** Everywhere else the rule holds: a
message body never lands in a log or the database. The exception exists
because those questions are the to-do list for new kinds — the group asks
what it asks, and guessing at it from the log's `kind=unknown` is how
"vem är bäst?" went unanswered for a day. What is kept is the text and the
time; not who asked, so nothing ties a person to their words. It is swept
after thirty days because it is a list to act on, not a record, and read
with `wordleland questions list`. The privacy notice says so. A question
the model failed on altogether is kept too: whatever it was, the bot did
not answer it. "Unknown" also covers a greeting or a remark that asks
nothing, so the list has some chatter in it; it is short-lived and read by
one person, which is cheaper than a third kind for the model to confuse.

**Asking what the bot can do is a kind, and a misread is one line.** "Vad
kan du?" used to be `unknown` and got the list only by fallback, counted
among the questions the bot could not place. It is `help` now. And a
question the bot cannot place gets one short line pointing at "what can
you do?" rather than the whole list: this is a chat, and a bot that
answers every misread with a paragraph is a bot people stop asking.

**Rule explanations are catalogue text, not model text.** "What counts as a
miss?" is answered by a `rules` request with a topic from a fixed list, and
the topic's explanation is written by hand in every language to say what
`internal/stats` actually does. The tempting alternative — let the model
explain from a rules document — is exactly where a small model starts
inventing, and a bot that explains the scoring wrongly is worse than one
that names the wrong leader: the leader can be checked on the board, the
rule is the thing people would check it against. A rule that changes in
code changes its sentence here, in every language.

**"Can Bo still catch up?" is arithmetic with one stated assumption.** The
month's final averages all share one denominator — the month's days, a day
not played scoring 7 — so the average a chaser needs over their remaining
days is exact: the leader's average times the month's length, less the
points the chaser has scored, over the days they can still play. Today is
one of those days only for somebody who has not played it. The one thing
the answer assumes is that the leader keeps their pace, and the sentence
says so. It does not model the leader slipping or the chaser's own
history; a needed average under 2 is called what it is — near-perfect
rounds — and under 1 the pass is impossible, since a 1 is the best a day
can be. Those two thresholds are the whole of the judgement; everything
else is the number.

**A past month is spoken of as the 🏆 message spoke of it.** "Vem vann
juli?" gets the month's result in the announcement's own words, not a
present-tense "leads": the month is over, and the answer to who won it is
the sentence that announced it. The model supplies the month as YYYY-MM,
worked out from today's date; a named month overrides whatever span it
also wrote, since "who led July over the last 7 days" means nothing.

**Counts and habits read what the board and the recap already read.**
"How many 2s do I have" is the board's distribution; "who posts first" is
the recap's posting order over its thirty-day window. "When do I usually
post" is the one new figure — the median of the player's recent posting
times, chosen over the mean because one late night should not move it.

**A day's score and monthly wins are the same shape.** The model supplies
a date (worked out from the day it is given, so "yesterday" and "July 5"
resolve) or nothing; the store has the result, or does not. Wins come from
`stats.ComputeSeason`, which counts closed months only, so the month in
progress hands nobody a title.

**"Last N days" got the month's rules.** `stats.ComputeRecent` ranks a span
of puzzles ending today the way `ComputeMonths` ranks a calendar month — a
concluded day not played is a failure, ties share a place — because "who is
best this week" is the same competition on a shorter window, and a version
where turning up on your good days wins would contradict what the month
says about the same players. It returns a `stats.Month` with the calendar
fields zero rather than a new type carrying the same eight fields; the
alternative was a type whose only distinction was saying "not a month".

**Every call to the model says how it is to be run, rather than trusting
the model's defaults.** Trying newer models (qwen3, qwen3.5) on the
production VM showed three defaults that hurt. A thinking model reasons
at length before a one-line answer, tens of seconds on a CPU, so a model
the server lists as able to think is sent `think: false`, and reasoning
that comes back in the answer anyway is stripped. Ollama's context on a
CPU is 4096 tokens, and a longer prompt is cut from the front — the
instructions first — with a 200 and a warning only in its own log; every
call, the warm-up included, asks for 8192, one number throughout since a
request asking for another size reloads the model. And qwen3.5 ships a
`presence_penalty` of 1.5, which discourages repeating what the prompt
says: the quotes and field names a request is made of. It is set to 0.
The model is loaded at startup rather than at the first question, and the
fixed part of the instructions comes before the asker and any quoted
post, since the server reuses its work on a prompt only up to the first
byte that differs.

**The bridge contains a failed answer.** An answer runs in its own
goroutine, which the Supervisor's recover does not reach, and runs code
steered by a model's output; a panic there is recovered, logged, and
takes the typing indicator down with it. A question that waited more than
two minutes for its turn is dropped rather than answered to a
conversation that has moved on. And the bridge looks a quoted post up in
the same Signal client the app posts with: with a client of its own, it
had sent nothing, and a reply to the bot's post never carried the post
along.

**The model reads the question; Go writes every word of the answer.** A
language model writing the answer itself was tried at length — tool
calling, a persona, checks that every number and name in its sentence came
from a lookup, a rewrite when they did not — and run in the group with
qwen3.5 4B and 9B on the production VM's CPU. It did not hold up. With
thinking off, the 4B copied the persona's example lines into its answers
word for word; the 9B answered the leader as if he were a chaser, with no
figures in the answer for the checks to catch; each answer took a minute
and more. The checks could stop a wrong number, never make a small model
write well. So the direction is the one this section started with, taken
further: the model places a question, and for every question the group
asks there is a kind that Go answers from stats and the catalogue writes.
The attitude is the catalogue's: short asides written once, by hand, true
every time.

**What the group asks decided the kinds.** Beyond who leads and how one
player stands, the questions that came up, or will, are about the race
("if Martin gets a 6 tomorrow, who wins?"), about two players ("how do I
stand against Anton?"), about one player in full ("tell me about Bo",
"roast Alma"), about days and puzzles ("how did everyone do yesterday?",
"what was the hardest word in August?"), and about the group ("what are
the records?"). Each became a kind with its own answer:

- *What if* scores a made-up result into the month as that player's next
  day, by the month's own arithmetic, and leaves everyone else where they
  stand — and says so. That is the question as asked: what that one result
  would do, not a forecast of every day until then. Its day is today, or
  tomorrow once a named player has played today's.
- *Versus* is both players' places over the span and, on the days both
  played, who scored better. Two in three won is the answer saying the
  duel is somebody's.
- *Day wins* count each day's best score as a win for everyone who had it,
  on days at least two played and somebody solved: alone, a player beats
  nobody.
- *A day* and *the hardest puzzle* use the daily recap's terms for hard and
  easy — three quarters of a guess from the usual, and at least three
  results — which moved to `internal/stats` so both read one definition.
  The hardest puzzle leaves today out, whose average moves with every
  result.
- *Form* and *steady* read the board's form window and spread, and a
  player's form is called up or down on the board's own significance.
- *Weekday* averages every result by day of the week once each day has ten
  results behind it (four for one player).
- *Profile* is everything about one player at once, and it is also the
  answer to "roast": the tease is the trait the board already gives them,
  with its reason. *History* is their last six months.
- *Records* count finished months only — a month three days old has an
  average nobody has had to hold — and *the fewest* of a score is among
  the ranked, so a newcomer with three games is not the answer.

**Nothing ranks absence.** No kind names who plays least, misses most or
has gone quiet, for the reason the recaps never tease a missed day: a
result somebody posted is theirs to be teased about, not posting is not.

**One list of kinds feeds the schema and the parse.** The schema's enum
and parseRequest's check were two lists that had to be kept equal; a
test now also fails when the prompt does not mention a kind or a span.
The prompt grew from about 900 tokens to 1,500. On a model whose server
can reuse its work on the fixed part of a prompt, that is read once, not
per question; the asker and any quoted post stay after it.

**A message that asks two things is two requests from one call.** "Who
leads, and is my streak still going?" used to be answered half. The
request gains `also`: up to two further requests of the same shape, one
level deep, from the same call to the model; each is answered in turn and
the answers go out as one post. A tool-calling agent could do the same by
looking each part up, but that costs a round of the model per lookup and
a round to finish, which on a CPU is the minute per answer this section
already turned down. A greeting or a thank-you among the further requests
is dropped, a repeated one is kept once, and a thank-you ahead of a
question gives way to it.

**Models are compared by a placing test, not by impression.** Choosing
between qwen2.5, qwen3, qwen3.5 and gemma4 kept coming down to reports
from elsewhere and a question or two in the group. `wordleland
placing-test` asks a model a fixed set of questions and scores how many it
places as they should be, comparing only the fields each question is
about — "vem leder?" is right whatever span it gets, a versus is right
with its two players in either order. The roster, the day and the asker
are made up and fixed, so every expected name and date is known, and it
runs without the database. A unit test holds that every kind has a
question and that each question's own answer would pass, so a miss is
the model's, not the test's.

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

## The admin area's settings screen is read-only, and says so per row

`internal/web/admin_settings.go` shows every environment variable the code
reads, with what it came to — values in force rather than what was typed, so
a default that is doing the work says so. It changes none of them, and it
never will: these are read where the process is started, and a screen that
let an admin type over one would be writing somewhere the next restart does
not read. *Since the fifth pass the rows are in the design's four cards —
the app, Signal (the bot's language model with it), the admin's sign-in, mail
— each saying how much of it is set, under one line with a lock saying the
environment is read-only here; the lock no longer trails each row.*

Secrets are reported, never shown: `config.Setting` carries a *kind* — unset,
a value, a secret, on, off — and the words for those live in the catalogues,
because this package has no translator and should not grow one.

The two Signal identifiers are shown in full here, as on Diagnostics
(53abcda). They were masked here at first, on the argument that a phone
number on a screen nobody reads it for is personal data with no reason to
be there. But only an admin reaches either screen, and an admin already has
both from Diagnostics; masking one of two screens hid nothing from anyone.

## The board's languages are files, not a list in code

`internal/i18n` reads whatever is in `locales/`, and the picker puts English
first and sorts the rest, so adding a language is adding a file. What was not
free is number formatting, which is a table of locale to separators rather
than a Swedish special case — comma before the fraction, space between
thousands — so a new language that writes numbers differently adds a row.
English groups with a comma. It went ungrouped for a long time, because the
numbers it mostly formatted were puzzle numbers and `1918` reads better than
`1,918`; those go through `Identifier` now, which never groups in any
language, so a count can be written the way each language writes one:
"2,153 puzzles" and "2 153 pussel".

That a language is cheap to add is not a reason to keep one. German, Spanish
and Italian shipped for a while, for the fun of it, and were taken out again:
every new sentence was four translations to write and check rather than one,
and that was not worth it. English and Swedish are the languages the
board is kept in. A reader whose saved choice was one of the three falls
back the way any unknown locale does — to the browser's language, then
English — without a migration.

Two tests in that package earn their place once there is more than one
translation to keep in step. A key English has and a translation does not
falls back silently — one English sentence in the middle of a Swedish page,
nothing failing, nothing logged, noticed only by somebody reading that page
in that language. And `fmt` verbs are positional, so a translation carrying a
different set of them either drops an argument or prints `%!d(MISSING)` onto
the page.

Every sentence the server says is a key, the error page's included. That
page was the last place English was written in Go — "There is nothing at
this address" was a literal in `renderError`, and so were nine rejections on
the sign-in, code and reset screens — so a Swedish reader met English
precisely when something had gone wrong. The keys are namespaced per screen
(`signin.error.credentials`, `totp.error.wrong`, `error.notFound.body`),
following what `recovery.error.*` already did, rather than one shared bucket:
the same word is not the same sentence on two screens. A test reads a 404 in
Swedish and a rejected sign-in in Swedish, so a literal cannot
come back unnoticed.

## What only a browser can check

`go test ./...` tests the markup the server sends. What a browser makes of it
— whether following a link reloads the document, where the page is scrolled
to afterwards, where focus went, whether two headings are the same height —
is not in the HTML, and seven bugs in a row shipped past the suite because
of it: a page that arrived 56px down, a title 45px in, a subtitle 8px off, a
menu anchored to half a bar, a container query written before the rule it
overrode, a delete drawn in the safe colour, focus landing on a hidden copy
of the rail. Every one was found by driving a browser by hand.

`internal/web/browser_test.go` drives one for real. It is behind a build tag
so the ordinary loop stays fast and needs nothing installed; `go test -tags
browser` starts the app on a real port, launches whatever Chrome is on PATH
headless, and talks to it over the DevTools protocol. It runs as its own CI
job, on the Chrome the runner already has.

**No new dependency, and the rule stands.** The plan was to take a small
websocket client for this, as an exception scoped to test-only code. It
turned out not to be needed: the Signal bridge already depends on
`gorilla/websocket`, and the harness uses that. So the argument this
document said a headless browser would need has been made, and the answer
is that it costs nothing the module did not already carry — no npm, no
bundler, no browser download, no package. Should the bridge ever drop that
dependency, the harness is the one other user of it and would go with it or
carry it alone; either way it stays out of the production binary, which is
what the rule is for.

**What it asserts is what a reader would notice, not how the script does
it.** No reload; back goes back; a page starts at the top; the title does
not move; the dialog stays on the page; nothing in the console. The pages
are read off the bar and the pill row rather than listed, so a view
added later is covered without anyone remembering. That is deliberate: the
front end is about to change again, and these are the parity net for it —
they should pass unchanged against a different script doing the same job.

**What ends a switch is the old content leaving, not the address changing.**
The first version of the harness waited for the address to match, and one
run in six on a phone found no drawer to press: the row for the page already
open matches before its fetch has landed, and the next press opened a drawer
on a body about to be swapped away. So a press marks the `<main>` on screen
and waits for that node to be gone — which is what a switch is, whichever
script does the swapping. The three green runs before that were luck, and
the whole difference between a suite that is trusted and one that is
ignored is one flake.

**Four more, after the first six.** A link to a page that does not exist
shows the error frame in place, with no rail, and Back brings the rail back
— the body-swap design exists partly for this. The drawer on a phone closes
on its own backdrop, pressed with the mouse at a point, because what is
stacked where is the one thing a selector cannot vouch for. The search
overlay opens from the keyboard, answers as you type, and Esc puts focus
back where it came from — the behaviour AGENTS.md used to name as having
nothing to fall back to and no test. And following a theme link changes the
theme without a reload: the theme lives on `<html>`, outside the body the
switcher replaces, and is carried across by hand.

**What it deliberately does not do.** It does not screenshot: pixels change
with every font hint and there is nobody to say which change was wrong.
It does not test the no-JavaScript story, which is the rest of the suite's
job and which a browser with script disabled would only re-prove. And it
does not run under `go test ./...`: a suite that is slow or environment-
bound trains people to skip it, and this one's whole value is in being run.

**It was the parity net it was written to be.** The front end did change
again — the hand-written page switcher became htmx's boost, see *htmx does
the fetching* — and the suite ran unchanged against the new script except
where it had to be told about a rule htmx has and the old script did not
(a link a test writes into the page has to be handed to htmx). Two
assertions were added ahead of that change and confirmed against the old
script first: where focus lands after a switch, and that collapsing the
rail neither reloads nor forgets. A third, that posting a form does not
reload either, is new behaviour and was added with it.

**The glass redesign took the rail with it, and the net held.** The tests
that were about the rail itself — its collapse, its width before the reply,
its name in the view transition — went with it, and the drawer's backdrop
test became the phone's page menu closing on a press outside it. Everything
else ran against the new shell once the harness read the bar instead of the
rail. Five pins were added for what the redesign introduced and a selector
cannot see: that the glass frosts and nothing is named for the transition
(the two fail together, which is the point), that the page scrolls under the
bar, that nothing sits at the bottom of a phone, that the bar fits its window
in both languages, and that the current pill is in view without the page
having moved. Each was checked red against the behaviour removed before it
was trusted green. What none of them can see is how the glass looks — the
blur, the highlight, the dark theme's shadow. That was looked at in headless
Chrome at a desktop and a phone width, in both themes, and by hand in Safari
on a Mac and an iPhone after the redesign merged — the engine the design was
drawn for, and whose backdrop-filter and safe-area handling this most depends
on. By hand only: the suite drives Chrome, so a regression that shows in
Safari alone, like the focus ring a script-set focus draws there (see *The
sweep before merging*), is a person's to catch.
