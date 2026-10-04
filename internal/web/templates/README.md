# Template layout

`base.html` is the page skeleton — the `<!doctype html>` shell, the
`title`/`content` blocks every page fills in, the footer, and the application
shell it wraps them in. Every other file in this directory is a page
(`today.html`, `board.html`, one per route), and a page renders only its own
content: the bar and the footer are assembled once, in `base.html`, rather
than each page remembering to call for them.

`chrome.Frame` picks which of three arrangements a page is wrapped in.
`"app"` is that shell: the glass bar floating over the page, the page, and
the footer. `"auth"` is the sign-in family — the card on its field of score
tiles, a footer carrying the language, and no bar at all, because there is
nothing yet to navigate. `"bare"` is an error page: the `<main>` and the
footer, since a full navigation wrapped around "there is nothing at this
address" offers the application to somebody who has not got it.

`ui/` and `app/` hold shared partials, split by one test:

**Does rendering this need `chrome`-shaped data, or a concept specific to
Wordleland (accounts, admin, the Wordle score ramp, the app's exact set of
supported locales)?** If yes, `app/`. If it would make sense unchanged in
any server-rendered `html/template` app, `ui/`.

`parseTemplates()` (in `internal/web/templates.go`) parses every page
together with `base.html` and every file under both `ui/` and `app/` —
Go's template namespace is flat regardless of which file a `{{define}}`
lives in, so a page's `{{template "x" .}}` call sites don't change when a
partial moves between files.

A struct-shaped partial (`chip`, `badge`, `stat-list`, `pill-nav`) needs
its data built at the call site, and `html/template` only passes one
pipeline argument. `dict` (also in `internal/web/templates.go`, registered
on every page's `template.FuncMap`) builds that argument from alternating
key/value pairs: `{{template "chip" (dict "Label" . "Dashed" true)}}`. It
returns `map[string]any`, which `.Label`/`.Dashed` read the same way they'd
read fields on a struct.

## `ui/` — domain-agnostic

| Partial | What it is |
|---|---|
| `chevron-down`, `lock-icon`, `github-icon` | The three glyphs still drawn by name — `icons.html`. Every other icon is a Material Symbol, drawn by the `symbol` and `codeSymbol` template functions (see *Icons*). |
| `pill-nav` | One active choice among several, as a row of pills. |
| `switcher` | The head of a page with siblings, and the row of pills that moves between them. |
| `page-empty` | A page with nothing in it yet: one card with a glyph, what is missing and what will fill it, in place of everything the page would draw. Defined beside `switcher`. |
| `toast` | The outcome of something just done, in frosted glass — over the foot of a wide window, under the bar on a phone, whose foot is the browser's: a check, the text, and a close that is a link to the page without the notice. Defined in `ui/toast.html`. |
| `empty-state` | A list with nothing in it yet, inside a card that has other things: a glyph in the accent's wash, what is missing and what will fill it. `page-empty` is the whole-page version. Defined in `ui/empty-state.html`. |
| `admin-head` | An admin screen's page head, then the five sections as a segmented bar of tabs; on a phone the bar is hidden and the eyebrow leads back to the section list at `/admin`. Defined in `app/admin.html`. |
| `pattern` | The squares a result was shared with, small beside a name or larger on the Puzzle page. Built from a stored `wordle.Grid` by the `gridRows` template function; nothing is drawn for a result without one. |
| `progress-bar` | A filled track, with a `compact` size and a `win` fill modifier. |
| `stat-list` | A label/value `<dl>`, in three visual variants (`figure`, `row`, `admin`). |
| `button` | A link styled as a control (`.btn` — see *Controls* below). |
| `chip` | A tag on something else — a reason, a retirement notice, an activity kind. |
| `badge` | A status of the thing itself — on/off, remaining/none. |

**`button` is not called from any page template yet.** It exists so a
future page that needs a link styled as the app's button has a canonical
target instead of inventing one on the spot — until then it's dead code
the template parser touches but nothing renders, which is expected, not a
bug. Every other partial in the table is wired in (see "Migration
complete" below).

They replace patterns already duplicated across pages under different
names:

- `pill-nav` replaces `.view` (topbar/tabs) and `.span` (grid time-span
  picker) — the same "one active link among a row of links" pattern twice
  over. It had two more callers, the admin tabs and the player picker, and
  both are now `switcher` instead: a row of links only works while the row
  fits, and neither of those did — five admin sections wrapped to a second
  line on a phone and fourteen names scrolled sideways. `.seg` / `.seg-opt` was a fourth,
  left alone as a genuinely different visual — a bordered strip with
  internal dividers rather than a loose row of pills. Its one user was the
  board's All / Hard mode pair, which is now a row inside the ranking menu,
  so the pattern is gone rather than shared.
- `progress-bar` replaces `.dist-track`/`.dist-fill` (the player page's
  score distribution) and `.bar`/`.bar-fill` (the months table's bar
  column). Both were driven by a literal `style="width:NN%"` — a
  per-instance number, not a hardcoded design value, but still a `style=`
  attribute in the templates the Phase 1 audit flagged. `progress-bar`
  sets a `--pct` custom property instead and lets CSS compute the width,
  so nothing in the partial itself is a magic number.
- `stat-list` replaces `player-stats`, `month-stats`, `admin-figures` and
  `signin-stats` — four names for the same `<dl>` of label/value pairs.
  They render three genuinely different ways (a large right-aligned
  figure, a compact label-left row, or a bordered footer strip), so
  `stat-list` takes a `Variant` rather than picking one look and losing
  the other two — collapsing the *visual* differences without a rendered
  before/after would be guessing, the same call made for the remaining
  spacing clusters in `internal/web/static/README.md`.
- `chip` and `badge` are two names that already meant two different
  things (`chip` annotates something else, `badge` reports a status of
  the thing itself) — kept as two partials, not merged into one with a
  variant, because the roles are real, not accidental.
- `button` wraps `.btn` rather than inventing a second one nobody asked
  for.

## Controls

Every control on every screen is one of four things, told apart by two
classes. The rules live in app.css under *Controls*; this is the table to
write markup from.

| Class | Reads as | Use it for |
|---|---|---|
| `btn` | filled, accent | The one thing this group is for: a form's submit, the primary link out of a card. At most one per row. |
| `btn secondary` | outlined, neutral | A real control that is not that one: a cancel, an alternative, the submit attached to a single field. |
| `btn danger` | filled, red | The press that commits something destructive — only ever inside the question that asked first. |
| `btn secondary danger` | outlined, red | The press that opens something destructive, or one that does a destructive thing outright without asking. |

So an act that cannot be undone reads the same wherever it appears: an
outlined red control opens the question, a filled red one inside it commits.
Rotating the share slug, rotating a two-factor secret and turning two-factor
off are all that shape, and the tone follows the consequence rather than the
screen — the same enrolment control is `btn` when setting a first secret up
and `btn secondary danger` when replacing one, because only the second one
costs anything.

Whether an act asks first is a separate question from how it is toned. It
asks when what it breaks is not immediately in front of the reader — the share
slug breaks everyone else's links, turning two-factor off removes a factor —
and does not when the result is the next thing on screen: discarding a held
result, generating recovery codes. Either way the opener is outlined red;
only the press inside a question is filled.

Anchors and buttons take the same class and come out the same object. Use an
anchor when the press is a navigation (a cancel that goes back, a control
whose first press only changes what the page shows) and a button when it
posts.

**What is not a control.** `link` is a prose link — a link inside or beside a
sentence, underlined, accent-coloured — and never goes on a `<button>`: a
control that looks like prose is a control nobody can find. The bar's pages,
menu rows, pills and the pill row's step arrows, settings tabs, the segmented
theme control, `chip`, `badge` and `.toggle` are navigation and selection;
they carry their own rules and are not part of this table. The account menu's
rows are drawn by being rows, whatever element they are, which is why signing
out is a `<button class="menu-row danger">` there.

Two tests hold this together: `TestEveryControlIsOneOfTheFour` walks the
screens and refuses a `<button>` with no control class or with `link` on it,
and `TestADestructiveActAsksBeforeItActs` pins the open-then-commit pair.

**Not built:** a table-scroll wrapper. `.table-scroll` (in app.css) is
already just a CSS class on a wrapping `<div>` around a `<table>` — Go's
`{{define}}` has no clean way to wrap caller-supplied block content, and
forcing one into existence for `<div class="table-scroll">...</div>`
would be more template machinery than the pattern is worth. It stays a
plain CSS-class convention: wrap a scrolling table in
`<div class="table-scroll">`, nothing more.

## `app/` — Wordleland-specific

| File | Partials | Why `app/` |
|---|---|---|
| `topbar.html` | `mark`, `topbar`, `preferences`, `language-row`, `about` | The brand mark, and every reader of `chrome`: the pages, account state, search path, admin flag. |
| `trait.html` | `trait` | A Wordle result trait and its explanation. |
| `admin.html` | `admin-warning` | Admin-only chrome. |
| `last-five.html` | `last-five` | A player's last five days as score tiles, each opening its puzzle and date: the leaderboard and Today. |

Two page-local partials live beside the page that uses them rather than
here, since nothing else draws them: `day-popup` in `player.html`, what a
day on a player's page opens — the result, the group that day, and the day's
Puzzle page — and `search-results` in `search.html`, which the ⌘K overlay
renders alone.

The bar lists the views twice and defines them once. A wide window shows
every view in the capsule; a phone shows the page you are on and opens the
rest from it, and both are rendered from `.Nav`, with the stylesheet showing
one. Two lists of the same destinations is how one of them goes stale.

The admin area is not a view: it is one row in the account menu, and which of
its five screens you are on is `admin-head`'s tabs on a wide window and the
section list at `/admin` on a phone. The tabs are built by the same
`switcher.go` as the roster's pills, from `AdminTabs`, and carry the same
glyph and count; they are drawn apart because the design draws them apart —
a segmented bar, not a row of pills. `switcher` draws the roster: each pill a
rank and a latest score. `switcher.go` builds each from the list that
already exists (`AdminTabs`, the board), so there is still one place that
knows what the admin area contains and one that knows who plays.

The switcher renders the page head — eyebrow, `<h1>`, the line under it, on
the canvas — and the pill row as a card under it, so a page calls it ahead
of its content cards rather than inside one. Every other page writes the
same `<header class="page-head">` itself: one shape, so the title stands in
one place on every page. Nothing sits in front of it.
The step arrows beside the row are links to the neighbours on either side,
and they wrap, so neither is ever a disabled control.

`preferences` is the theme and the language, and both menus the bar can open
— the account's and the guest's — render it, so there is one definition of
each control. `about` is the About dialog, in the same two menus and, with
`Link` set, in the footer, where its summary is a footer link. It carries
`name="about"` rather than joining `menu-group`: it opens from inside a menu,
and a shared group would close the menu it lives in, taking the panel with
it.

## Icons

Material Symbols Outlined, the design system's set, as inline SVG:
`{{symbol "search" 21}}` draws a glyph by name at a size, and
`{{codeSymbol .Code 19}}` draws the one for a view, a page, a kind of search
hit or a theme. The path data is in `symbols_data.go`, copied from the
upstream SVGs; adding a glyph is copying its path in. A glyph with a filled
form carries both, and app.css fills whichever is current.
`TestEverySymbolExists` reads the names the templates ask for and holds them
against the data, because `symbol` draws nothing for a name it does not know.

## Migration complete

Every page template and shared partial now goes through the `ui/`
partials above rather than hand-rolling the patterns in the table: the
chip and badge sites, the three `stat-list` variants, the two
`progress-bar` bar families, and every pill-picker (`.view`, `.pick`,
`.span`) all across today, board, months, grid, player, the admin
screens, the auth screens and `topbar.html` itself. Every raw class they
replaced (`.picker`/`.pick`, `.spans`/`.span`, `.view`, `.dist-track`/
`.dist-fill`, `.bar`/`.bar-fill`, `.player-stats`/`.month-stats`/
`.admin-figures`/`.signin-stats`) is deleted from `app.css` — none of
them has a caller left.

Two things worth knowing if you're reading the git history rather than
just this file: `pill-nav-item`'s CSS didn't originally match every raw
class it replaced pixel-for-pixel (`.pick` and `.span` had different
padding, border and active-state treatments), and `pill-nav`'s own
container padding/border-bottom was missing entirely for one commit
before a browser catch fixed it. Both were reviewed and accepted in a
browser page by page as each slice landed. `pill-nav` also had a
wrapper-less `pill-nav-items` alongside it, for the two callers in the top
bar that could not take its `<nav>`; both were the view switcher, and it
went when the views moved to a rail, which has since gone too.
