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
| `theme-icon`, `chevron` | Small inline SVG icons — `icons.html`. No app data. |
| `pill-nav` | One active choice among several, as a row of pills. |
| `progress-bar` | A filled track, with a `compact` size and a `win` fill modifier. |
| `stat-list` | A label/value `<dl>`, in three visual variants (`figure`, `row`, `admin`). |
| `button` | A link styled as the app's one button treatment (`btn-primary`). |
| `chip` | A tag on something else — a reason, a retirement notice, an activity kind. |
| `badge` | A status of the thing itself — on/off, remaining/none. |

**`button` and `badge` are not called from any page template yet.** They
exist so a future page that needs them has a canonical target instead of
inventing one on the spot — until then they're dead code the template
parser touches but nothing renders, which is expected, not a bug.
`pill-nav`, `progress-bar`, `stat-list` and `chip` are all wired in; see
the Phase 3 migration status below for which page uses which.

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

## Phase 3 migration status

Page templates keep their own markup until they're migrated to the shared
partials above, one page at a time, each its own commit:

- **today.html** — migrated. Both `.chip` call sites (the "still out" list
  and a benched player's reason) now go through the `chip` partial.
- **board.html** — migrated. Its one `.chip` call site (a row's reason,
  e.g. "on leave") now goes through the `chip` partial. `board.html` also
  serves the read-only share view of the leaderboard (`board.go` renders it
  for both authenticated and `/share/...` requests via `chrome.ReadOnly`),
  so this covers that view too.
- **months.html** — migrated. The winner-pane figures now go through
  `stat-list` (`Variant: "figure"`, matching `.month-stats`'s CSS exactly),
  each month row's average bar goes through `progress-bar` (`Compact:
  true`, matching `.bar`'s sizing exactly), and the "played, not ranked"
  chip list goes through `chip`.
- **grid.html** — migrated. The time-span picker (`.spans`/`.span`) now
  goes through `pill-nav`. `pill-nav`'s item styling (`pill-nav-item`)
  still doesn't match `.span`'s pixel-for-pixel — different font size,
  padding, border treatment and active-state background (see `app.css`'s
  `.span` vs `.pill-nav-item` rules) — a deliberate divergence per this
  file's own `pill-nav` rationale above, not checked in a browser.
  `.pill-nav`'s *container* padding/border-bottom, however, was a real
  gap caught by browser testing on player.html (below) and fixed for both
  pages at once. `.span`/`.spans` stay in `app.css` — they're still used
  by `admin_activity.html`, not yet migrated.
- **player.html** — migrated. The player picker (`.picker`/`.pick`) now
  goes through `pill-nav`; `playerTab.Name` was renamed to `Label` to
  match `pill-nav`'s item shape. Both `.chip` sites (retired, benched
  reason) go through `chip`. `.player-stats` goes through `stat-list`
  (`Variant: "figure"`, byte-identical CSS, confirmed same as
  months.html's case). The distribution bar (`.dist-track`/`.dist-fill`)
  goes through `progress-bar` (default, non-compact — byte-identical CSS
  match, confirmed the same way as `.bar`'s compact case in months.html).
  `pill-nav-item`'s per-item styling still doesn't match `.pick`'s
  pixel-for-pixel (same caveat as grid.html's span picker) — not checked
  in a browser. Its *container* styling was missing entirely: `.pill-nav`
  had no `padding-top`/`padding-bottom`/`border-bottom`, where both
  `.picker` and `.spans` did (identical values in both) — caught by
  browser testing and fixed by adding those three declarations to
  `.pill-nav` itself, which also fixed grid.html's span picker.
- **Admin screens** (`admin_activity.html`, `admin_activity_detail.html`,
  `admin_pending.html`, `admin_players.html`) and the shared `admin-tabs`
  partial — migrated. The admin-tabs strip now goes through `pill-nav`,
  fed by a new `chrome.AdminTabs()` method (the four destinations are
  fixed, so it builds the items itself rather than every handler passing
  the same slice). All three `.chip` sites (two activity-kind tags, one
  pending-result source) go through `chip`. The activity filter picker
  (`.spans`/`.span`, the last page still using it) goes through
  `pill-nav`. `admin_players.html`'s figures (`.admin-figures`) go through
  `stat-list` (`Variant: "admin"`, byte-identical CSS); `adminPlayerPanel`
  gained a `Figures []playerStat` field built in `adminPanel()`, replacing
  its separate `Games`/`Average`/`LastSeen` fields (reusing player.go's
  `playerStat` type rather than inventing a second one with the same
  shape).

  This was the last user of `.picker`/`.pick`, `.spans`/`.span`,
  `.dist-track`/`.dist-fill`, `.bar`/`.bar-fill` (the component classes,
  not `.months-table .bar-cell`'s layout rule) and `.player-stats`/
  `.month-stats`/`.admin-figures`, so all of that CSS is now deleted
  rather than left dangling — `TestStylesheetIsWhole`'s selector list was
  updated to match (`.pill-nav` replacing the three dead selectors it
  checked for). `.signin-stats` (login/invite) is untouched; it has
  nothing to do with this migration.

  Not verified: `pill-nav-item`'s styling still doesn't match `.pick`'s or
  `.span`'s pixel-for-pixel, and the admin-tabs strip's gap changes from
  4px to `pill-nav`'s 6px now that `.admin-tabs`'s own override is gone —
  same open caveat as grid.html and player.html, not checked in a
  browser.
- Everything else — not yet migrated; still hand-rolls `.chip` and
  `topbar.html`'s own `.view` switcher directly.
