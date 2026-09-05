# Template layout

`base.html` is the page skeleton — the `<!doctype html>` shell, the
`title`/`content` blocks every page fills in, and the footer. Every other
file in this directory is a page (`today.html`, `board.html`, one per
route). `ui/` and `app/` hold shared partials, split by one test:

**Does rendering this need `chrome`-shaped data, or a concept specific to
Wordleland (accounts, admin, the Wordle score ramp, the app's exact set of
supported locales)?** If yes, `app/`. If it would make sense unchanged in
any server-rendered `html/template` app, `ui/`.

`parseTemplates()` (in `internal/web/templates.go`) parses every page
together with `base.html` and every file under both `ui/` and `app/` —
Go's template namespace is flat regardless of which file a `{{define}}`
lives in, so a page's `{{template "x" .}}` call sites don't change when a
partial moves between files.

## `ui/` — domain-agnostic

| Partial | What it is |
|---|---|
| `theme-icon`, `chevron` | Small inline SVG icons — `icons.html`. No app data. |
| `pill-nav` | One active choice among several, as a row of pills. |
| `progress-bar` | A filled track, with a `compact` size and a `win` fill modifier. |
| `stat-list` | A label/value `<dl>`, in three visual variants (`figure`, `row`, `admin`). |
| `button` | A link styled as the app's one button treatment (`btn-primary`). |
| `chip` | A tag on something else — a reason, a retirement notice, an activity kind. |
| `badge` | A status of the thing itself — on/off, remaining/none. |

**`pill-nav`, `progress-bar`, `stat-list`, `button`, `chip` and `badge` are
not called from any page template yet.** They exist so Phase 3 (migrating
each page's markup) has a canonical target instead of inventing one
mid-migration. Until then they're dead code the template parser touches
but nothing renders — that's expected, not a bug.

They replace patterns already duplicated across pages under different
names:

- `pill-nav` replaces `.view` (topbar/tabs), `.pick` (admin tabs) and
  `.span` (grid time-span picker) — the same "one active link among a
  row of links" pattern three times over. `.seg` / `.seg-opt` (the grid's
  segmented control) is a genuinely different visual — a bordered strip
  with internal dividers and an inset ring on the active option, not a
  loose row of pills — so it's left alone rather than forced into this
  partial.
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
- `button` wraps the one button style app.css defines (`btn-primary`)
  rather than inventing a second one nobody asked for.

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
| `topbar.html` | `mark`, `flag`, `theme-picker`, `language-picker`, `topbar` | The brand mark, the app's exact two-locale flag set, and every reader of `chrome` (nav items, account state, admin flag). |
| `trait.html` | `trait` | A Wordle result trait and its explanation. |
| `admin.html` | `admin-warning`, `admin-tabs` | Admin-only chrome. |

## Icons

Inline SVG, `currentColor` fill/stroke, sized at the call site via the
`width`/`height` attributes already on each `<svg>` — there is no separate
size parameter because every existing icon is already used at exactly one
size. A future icon needing more than one size can take a size argument
then; adding one now with no second caller would be a guess.
