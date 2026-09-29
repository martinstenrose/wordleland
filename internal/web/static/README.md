# Design tokens

`app.css` is one file, embedded and served as a single request — there is no
bundler and this doesn't add one. Everything in it is built from the custom
properties defined at the top (`:root`, the `[data-theme="light"]` override,
and the `prefers-color-scheme` copy of it that covers `"system"`). This file
documents every token: what it's for, and — where the reason isn't obvious —
why its value is what it is.

**Where the values come from.** The palette, the type scale and the radius
scale are the Stenröse design system's, which lives outside this repository as
a Claude Design project; Wordleland was drawn against it there before it was
built here. What that means for anyone editing this file: a value that looks
arbitrary probably isn't ours to re-pick in isolation. What was *not* taken
from it is called out below — chiefly the inks on the score ramp, which the
design draws below 4.5:1.

Colours are `oklch`. That is the design system's own notation, and it is the
reason the alpha steps below can be one colour repeated at N% without the
muddy midpoints an `rgba` ramp gives over a tinted ground.

## Colors

| Token | Role |
|---|---|
| `--color-canvas` | Page background, behind every card |
| `--color-surface` | Card and panel background |
| `--color-surface-raised` | A second, lighter level used for insets: form fields, dividers, the sign-in aside, sticky grid headers |
| `--color-text` | Body text, and the colour the alpha ramp below is struck from |
| `--color-accent` | Links, the brand accent, focus rings |
| `--color-accent-strong` | Hover/active emphasis on accent-coloured elements. Within the brand hue: brighter in dark mode, deeper in light |
| `--color-accent-2` | The one hover the design system takes *off* the brand hue instead of brightening it, and the "worth a look" half of the diagnostics pair. Prose links only (`.link:hover`) — every link that is really a control carries its own hover |
| `--color-danger` | Destructive or broken: signing out, a diagnostic that has failed. The previous single-hue palette had no red to give these, so both borrowed the accent and read as emphasis rather than warning |
| `--color-danger-40`, `--color-danger-08` | Two steps of that hue, for the one place a warning needs a ground of its own rather than red ink on the page's: the question asked before something that cannot be undone (`.confirm`) |
| `--color-on-accent` | What stays legible on a field of `--color-accent` — the avatar, the button that closes a raised outcome. It tracks the accent rather than the text ramp, because the accent is light in one theme and deep in the other and the ink on it has to turn over with it. The score ramp's own ink happens to land on the same two values; an avatar is not a score and is not pinned to that ramp |
| `--color-better`, `--color-worse` | A form delta trending down (an improvement) or up. The design system spends the accent itself on "better" and `--color-danger` on "worse" rather than finding a fourth hue, so these are defined as aliases — named anyway, so that the rules drawing a trend keep saying "better" and "worse" rather than "accent". The ▼/▲ printed beside them repeats the meaning, so colour is never the only carrier |
| `--color-text-NN` | One colour at NN% opacity, used for borders, dividers, muted text, and faint hover fills. There is no separate token per role because there never was a consistent one — "border", "muted text" and "faint hover" already meant "text colour at some opacity". The design system spells the same thing with two tokens (`--border`, `--muted`); its `--border` is this ramp's `--color-text-12` over this surface, arrived at from the other direction |
| `--color-accent-NN` | Same idea over the accent hue — the "on" state of pickers, focus rings, hover fills |
| `--score-1` … `--score-6` | The guess-count fill ramp, one step per guess (`.cell.t1`–`.t6`, `.cal.t1`–`.t6`) |
| `--score-x` | A miss (`.cell.t7`, `.cal.t7`). Off the ramp, on the danger hue: a miss is not a seventh guess, it is the other outcome |
| `--score-ink`, `--score-3-ink`, `--score-ink-alt`, `--score-x-ink` | The text colour that stays legible on a score fill: `--score-ink` (the accent's own ink) on the full-strength fill of tiers 1–2, `--score-3-ink` on tier 3, `--score-ink-alt` (the text colour) on 4–6, `--score-x-ink` on the miss. Tier 3 is the one whose ink turns over with the theme — see the ramp below |
| `--color-border`, `--color-sep`, `--color-muted` | The design system's own names for a hairline, the rule between rows, and secondary text — the three roles the text-alpha ramp spells as steps, named where a rule drawn from the mockups wants the design's word for them |
| `--color-tint` | The accent's wash: a callout's glyph, the phone's page capsule, a filter pill in force |
| `--color-track`, `--color-seg` | A segmented control's track and its raised segment — the theme control, the settings tabs on a phone, the language list |
| `--grid-present` | The yellow of a letter in the word but in the wrong place, in a posted grid (`.pattern .sq.y`). A letter in place is the accent and one not in the word the border colour: the game's own three, not the score ramp's, since these are the game's squares |
| `--color-rail` | The ground a step down from a card's, for the pill row's card and the fade at its edge on a phone: a shade between the canvas and the surface, so the row reads as set back from the content under it |
| `--glass-bg`, `--glass-border`, `--glass-rim`, `--glass-shadow`, `--glass-filter`, `--glass-hover` | Liquid Glass, for what floats and nothing else: the bar's pieces, their menus, the figures' half of the sign-in card. A thin surface the page shows through, a hairline, light caught just inside it (`--glass-rim`, soft inset shadows on the piece's `::after` that follow the curve: along the top and bottom in the dark theme, all the way round with a faint shade in the ends in the light, as Safari's toolbar is), and a soft shadow under it — and the fill a pressed or current thing takes on it. `--glass-filter` is the blur and saturation behind it, painted on the piece's `::before` |
| `--glass-menu-bg`, `--glass-menu-border`, `--glass-menu-shadow`, `--glass-menu-filter` | The same glass, frosted harder, for what floats and carries text: a menu, the toast, the search overlay, the sign-in card. The bar's own pieces are the clearer strength above |
| `--scrim` | The veil over the page while the search overlay is open: warm and light in the light theme, darker in the dark |
| `--search-well` | The filled field the search overlay's input sits in on a phone, as iOS draws a search field |

**Why `--color-canvas` and `--color-surface` are two tokens:** every page is
a full layout of cards on a page background, so the card itself needs a
level distinct from what sits behind it — one token for each rather than
one token doing both jobs.

**Alpha steps and their opacities are listed in `app.css` itself**, not
duplicated here — the token block is the inventory. The two blocks do not use
the same opacities for the same step: dark text on a light ground needs more
of itself than light text on a dark one, and the steps are tuned per theme
rather than shared. Every step defined is consumed somewhere. A previous pass
dropped fifteen steps no rule ever used and added six that dark mode needed
but only the light blocks defined; `--color-text-07` survived that pass unused
and has since been dropped too.

**The score ramp is the design system's, and so is its formula.** A 2 is the
accent at full strength and each guess after it is less of it: 78, 52, 30 and
14 percent, mixed in sRGB into the raised surface. A 1 is drawn as a 2 —
there is no stronger green to give it. The formula is written once, in the
first token block: a custom property that uses `var()` is worked out on the
element that declares it, so each theme's accent and surface arrive through
the same line and a strong fill comes out deep in light mode and bright in
dark without a second ramp. Mixed in OKLCH instead, the middle of the ramp
walks through yellow on its way to a warm grey, and a 4 came out olive. The
fills are opaque, so a tile's border is its own fill.

The inks are where this departs from the design, which puts the accent's own
white on every tile down to 52%. Measured on the light canvas that is 3.2:1
at 78% and 2.2:1 at 52%, and every tile carries a digit that is the content
rather than decoration. So the pale half takes the text colour — from 78% in
light mode, and from 52% in dark, where the accent is bright enough to carry
dark ink at 78%. That is why tier 3 has an ink of its own. The full tile
carries white at 4.3:1 in light mode, a shade under 4.5; the digit is bold,
and the design's green was kept rather than darkened to clear it.

Before this ramp existed there were four tiers, and a 5, a 6 and a miss all
landed in the grey text ramp — so the three outcomes a player most wants to
tell apart at a glance were the three that looked alike.

## Shadow

`--shadow-elevated` is the design system's two-layer shadow: a 1–2px contact
shadow under a wide, soft one. It deliberately carries no extra hairline ring —
every consumer of this token already draws its own explicit `1px solid`
border, and adding a ring on top would double it rather than sharpen it. The
two themes carry separately tuned alphas.

## Border radius

`--radius-card` (22px) is the card radius, from the design's 20–24px, and
`--radius-card-lg` (24px) the header card's and a menu's a step above it.
`--radius-lg` (12px) is the panel radius inside a card and `--radius-md` (8px)
the old control radius, which buttons have left for `--radius-pill`. `--radius-xl` (14px) is a step above `--radius-lg`, for a
surface that floats free of the page rather than sitting in it — currently the
search palette. `--radius-xs` (3px), `--radius-sm` (6px), `--radius-pill`
(999px) and `--radius-circle` (50%) cover tiles, badges and pill-shaped
controls. `--radius-xs` and `--radius-sm` absorb a cluster of near-identical
values (2px/2.5px/3px, and 4px/5px/6px/7px/10px respectively) that had
accumulated with no visible reason to differ.

## Type scale

The scale is the design system's, in pixels: `--text-xs` (11px) for uppercase
kicker labels and table headers, `--text-sm` (13px) for secondary and table
body text, `--text-base` (14px) for body copy. It replaced a rem-based scale
whose four steps sat between 9.9px and 12.8px — close enough together that two
pairs of them collapsed into one step each on the way across, and everything
grew by about a pixel. The design system's own tables are 13px on 11px
headers, so the density this app needs survived the move.

The steps above `--text-base` — `--text-md` and up — are defined as the rules
that consume them arrive, rather than sitting in the block ahead of any use.
Most font sizes in the file remain plain literals: only the flagged
near-duplicate clusters were consolidated into tokens.

`--line-tight` (1.1) does the same for a three-way near-duplicate
(1.05/1.06/1.15) among tight heading line-heights.

## Spacing

`--space-card` is the horizontal gutter every card's direct children inherit
(`.card > *`), at 22px — the design system's card padding, and near enough to
the 22.4px it replaced that nothing reflowed.

Beyond that one token, the file still has several places using
near-but-not-quite-identical spacing values (e.g. clusters around 13–16px,
18–22px, 24–28px, 30–34px). Unlike the type and radius clusters, these are
spread across many unrelated one-off layouts (gaps, margins, section padding)
rather than repeating the same visual role, and collapsing them without a
rendered before/after is the wrong tradeoff to make blind.

## Fonts and transitions

`--font-body` is the Apple system font where there is one — SF Pro, which
Apple licenses only as the system font, so it is named in the stack and never
served — and Inter everywhere else, as the closest match to it. Inter is
served from here rather than from a font CDN: a page that reaches a third
party to finish rendering is a page this app does not control, and one more
party watching whoever reads the board. An Apple device never asks for it,
because the system font ahead of it in the stack answers first, so the bytes
go only where they are used. One variable file carries the whole 100–900
range, so a bold costs no second request; `font-display: swap` because the
board reads fine in the fallback and a blocked paint is worse than a reflow.
The file is the Inter project's own `InterVariable.woff2`, renamed only to
match the other lower-case paths, and `Inter-OFL.txt` beside it is the licence
the OFL requires to travel with it. It is served as `font/woff2` by name:
Go's own type table has no entry for it.

Every figure is tabular (`font-variant-numeric: tabular-nums` on the body):
scores, averages, dates and counts are read down columns, and a 1 narrower
than a 4 moves the column.

`--font-mono` is the monospace stack, which had quietly drifted into two
different forms (some rules omitted `SFMono-Regular`); both now share one
token. `--transition-fast` (`.12s ease`) is the older of the two short
transitions; `--transition-quiet` (`.2s ease`) is the glass bar's and the
controls': a pill lighting, a chevron turning, a switch's knob — short and
quiet, and never a spring.
`--transition-page` (`.16s`) is how long a page takes to become the next
one: the view transition every htmx swap runs. The browser pauses
rendering for the whole of it, so it is short.

Nothing is named for it. An element with a `view-transition-name` is a
backdrop root, and the glass inside a named bar frosts only its own empty
box, so the whole window cross-fades instead — the bar, the same pixels on
both sides of a swap, holds still through the blend anyway. The reasoning is
in `docs/decisions.md`, *The navigation floats*.

## One deliberate non-token

`.enrol-code img`'s white background is a literal `#fff`, not a token: a
TOTP enrolment QR code needs a true white quiet zone to stay scannable,
in every theme. Theming it would make it harder to scan in dark mode for
no benefit.
