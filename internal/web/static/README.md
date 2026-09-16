# Design tokens

`app.css` is one file, embedded and served as a single request — there is no
bundler and this reorganisation doesn't add one. Everything in it is built
from the custom properties defined at the top (`:root`, the
`[data-theme="light"]` override, and the `prefers-color-scheme` copy of it
that covers `"system"`). This file documents every token: what it's for,
and — where the reason isn't obvious — why its value is what it is.

## Colors

| Token | Role |
|---|---|
| `--color-canvas` | Page background, behind every card |
| `--color-surface` | Card and panel background |
| `--color-surface-raised` | A second, lighter level used for insets: form fields, dividers, the sign-in aside, sticky grid headers |
| `--color-text` | Body text |
| `--color-accent` | Links, the brand accent, focus rings |
| `--color-accent-strong` | Hover/active emphasis on accent-colored elements |
| `--color-better`, `--color-worse` | The only non-accent hues in the palette: a form delta trending down (green) or up (red). The accent ramp is one hue at varying strength, which cannot say "down" as against "up" — so trend gets its own pair. The ▼/▲ printed beside them repeats the same meaning, so the colour is never the only carrier |
| `--color-text-NN` | One color at NN% opacity, used for borders, dividers, muted text, and faint hover fills. There is no separate token per role because there was never a consistent one before this reorganisation — "border", "muted text" and "faint hover" already meant "text color at some opacity" |
| `--color-accent-NN` | Same idea, over the accent hue — used for the "on" state of pickers, focus rings, hover fills |
| `--score-1` … `--score-4`, `--score-3-border`, `--score-4-border` | The guess-count fill/border ramp (`.cell.t1`–`.t7`, `.cal.t1`–`.t7`) |
| `--score-ink`, `--score-ink-alt` | The text color that stays legible on a score fill. They differ because a tier-1/2 fill and a tier-3 fill need different contrast — in dark mode `--score-ink` happens to equal `--color-surface` exactly, so it references it instead of repeating the hex |

**Why `--color-canvas` and `--color-surface` are two tokens:** every page is
a full layout of cards on a page background, so the card itself needs a
level distinct from what sits behind it — one token for each rather than
one token doing both jobs.

**Alpha steps and their opacities are listed in `app.css` itself**, not
duplicated here — the token block is the inventory. Every step defined is
consumed somewhere; a previous version of this file defined 15 steps no
rule ever used (dead weight, since removed) and, separately, silently
*omitted* six steps that dark-mode rules actually depended on — those six
(`--color-text-70`, `-42`, `-07`, `--color-accent-60`, `-45`, `-80`) were
only ever defined in the light/system-light blocks, so several components
(section headings, menu rows, the settings switch's checked state, two
sparkline strokes) rendered with no color at all in dark mode. Both bugs
are fixed here: dead steps dropped, missing ones added to the base
(dark) `:root`.

## Shadow

`--shadow-elevated` is `0 16px 40px` at `.65` alpha in dark mode. It
deliberately carries no extra hairline ring: every consumer of this token
already draws its own explicit `1px solid` border, and adding a ring on top
would double it rather than sharpen it. Light mode keeps its own, separately
tuned value.

## Border radius

`--radius-md` (8px) and `--radius-lg` (14px) are the card and panel corner
radii. `--radius-xs` (3px), `--radius-sm` (6px), `--radius-pill` (999px)
and `--radius-circle` (50%) cover avatars, badges and pill-shaped controls.
`--radius-xs` and `--radius-sm` absorb a cluster of near-identical values
(2px/2.5px/3px, and 4px/5px/6px/7px/10px respectively) that had accumulated
with no visible reason to differ.

## Type scale

`--text-2xs` (.62rem) was already the dominant, consistently-used size for
uppercase kicker labels before this reorganisation — it's given a name
here rather than changed. `--text-xs` (.68rem), `--text-sm` (.74rem) and
`--text-md` (.8rem) each consolidate a cluster of near-identical sizes
(e.g. .66/.68/.69/.7rem, or 12px/12.5px/13px) that had no reason to differ.
`--line-tight` (1.1) does the same for a three-way near-duplicate
(1.05/1.06/1.15) among tight heading line-heights.

Most font sizes in the file remain plain literals — only the flagged
near-duplicate clusters were consolidated into tokens; introducing a
token for every size in the file was out of scope for this pass.

## Spacing

`--space-card` is the horizontal gutter every card's direct children
inherit (`.card > *`); its value is `22.4px`. About two dozen rules had
hardcoded `22px` directly instead of using the token where they clearly
meant the same card gutter; those now reference `--space-card` too.

Beyond that one token, the file still has several places using
near-but-not-quite-identical spacing values (e.g. clusters around
13–16px, 18–22px, 24–28px, 30–34px) that were flagged in the design-system
audit but **not** consolidated in this pass — unlike the type/radius
clusters, these values are spread across many unrelated one-off layouts
(gaps, margins, section padding) rather than repeating the same visual
role, and collapsing them without a rendered before/after felt like the
wrong tradeoff to make blind. Worth another pass with visual verification
in hand.

## Fonts and transitions

`--font-body` and `--font-mono` are the two font stacks, both previously
hardcoded at every use site. The monospace stack had quietly drifted into
two different forms (some rules omitted `SFMono-Regular`); both now share
one token. `--transition-fast` (`.12s ease`) tokenizes the settings
switch's two transition rules, which were already identical but repeated
by hand.

## One deliberate non-token

`.enrol-code img`'s white background is a literal `#fff`, not a token: a
TOTP enrolment QR code needs a true white quiet zone to stay scannable,
in every theme. Theming it would make it harder to scan in dark mode for
no benefit.
