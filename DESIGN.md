---
name: relay
description: Planner/builder handoff for herdr agent panes
colors:
  bg: "oklch(98% 0.008 257)"
  surface: "oklch(99.6% 0.005 257)"
  grid: "oklch(93.5% 0.012 257)"
  border: "oklch(90.5% 0.014 257)"
  fg: "oklch(29.5% 0.037 259)"
  dim: "oklch(45.5% 0.035 257)"
  ink: "oklch(50% 0.215 262)"
  ink-deep: "oklch(38% 0.18 262)"
typography:
  display:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "clamp(1.3rem, 1.02rem + 1.05vw, 1.8rem)"
    fontWeight: 500
    lineHeight: 1.22
    letterSpacing: "-0.022em"
  headline:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "clamp(1.05rem, 0.95rem + 0.4vw, 1.3rem)"
    fontWeight: 600
    lineHeight: 1.65
    letterSpacing: "-0.015em"
  title:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.9375rem"
    fontWeight: 600
    lineHeight: 1.65
    letterSpacing: "-0.005em"
  lede:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "clamp(1rem, 0.96rem + 0.2vw, 1.125rem)"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "normal"
  body:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.65
    letterSpacing: "normal"
  body-sm:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "normal"
  small:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "normal"
  caption:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "normal"
  label:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.8125rem"
    fontWeight: 500
    lineHeight: 1.4
    letterSpacing: "0.02em"
  label-regular:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.65
    letterSpacing: "0.02em"
  wordmark:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "1.0625rem"
    fontWeight: 600
    lineHeight: 1.65
    letterSpacing: "-0.015em"
  name:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.65
    letterSpacing: "-0.01em"
  code:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.8
    letterSpacing: "normal"
  code-inline:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.875em"
    fontWeight: 400
    letterSpacing: "normal"
  code-inline-sm:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.8125em"
    fontWeight: 400
    letterSpacing: "normal"
  diagram-name:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "19px"
    fontWeight: 600
    letterSpacing: "-0.01em"
  diagram-label:
    fontFamily: "Fira Sans, system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    letterSpacing: "normal"
  diagram-mono:
    fontFamily: "Fira Code, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "16px"
    fontWeight: 400
    letterSpacing: "normal"
rounded:
  none: "0"
spacing:
  xs: "0.5rem"
  sm: "0.85rem"
  md: "1.5rem"
  lg: "2.75rem"
  xl: "4.5rem"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.bg}"
    rounded: "{rounded.none}"
    padding: "0.7rem 1.4rem"
    typography: "{typography.label}"
  button-primary-hover:
    backgroundColor: "{colors.ink-deep}"
    textColor: "{colors.bg}"
  section-bar:
    backgroundColor: "{colors.fg}"
    textColor: "{colors.bg}"
    rounded: "{rounded.none}"
    padding: "0.55rem 1rem"
    typography: "{typography.label}"
---

# relay

## Overview

**Creative North Star: "a drafting sheet for a machine that moves files."**

relay is three pieces and a loop: a CLI the planner calls mid-turn, a daemon that carries the
report back after that turn has ended, and a state directory both of them write to. That
asymmetry is the hardest thing about the product to explain in a paragraph and the easiest
thing to explain in a drawing. So the page is a drawing, and everything else on it is
annotation around the drawing.

The register is a technical document, not a marketing page. Hairlines instead of shadows,
squared corners everywhere, one accent used sparingly, and a monospace face carrying every
label because every label on this page is either a command, a filename or a node in a system.
Light paper is deliberate: relay lives in a dark terminal, and a page that imitates its own
subject adds nothing. The drawing is *about* the terminal, drawn on paper.

Density is high but never cramped. The page assumes a reader who is comfortable with a
schematic and does not need to be walked through one.

**Key Characteristics:**

- One diagram is the hero; the pitch sits beside it, and both occupy the first screen together.
- The blueprint grid belongs to the diagram alone — it is the drawing surface, not wallpaper.
- Sections are separated by solid inverted bars carrying their own name, never by hairlines.
- Squared corners, hairline borders, no shadows. Elevation is tonal or absent.
- Every mono surface has ligatures off, because every mono surface carries a literal command.

## Colors

**Primary**

- **Ink Blue** (`oklch(50% 0.215 262)`): the single accent. Outbound edges in the diagram,
  links, the primary button fill, focus rings. Nothing else.
- **Ink Blue Deep** (`oklch(38% 0.18 262)`): hover and active states on anything ink.

**Secondary**

- **Foreground** (`oklch(29.5% 0.037 259)`): body ink, node outlines, and the fill of every
  section bar. It is the page's darkest value and it is not black.
- **Dim** (`oklch(45.5% 0.035 257)`): secondary prose, inbound dashed edges, diagram
  sub-labels. Clears 4.5:1 on paper, so it is usable for real text and not just captions.

**Tertiary**

- **Grid** (`oklch(93.5% 0.012 257)`): the 26px drafting grid, and the fill behind inline
  `code`. The two uses are the same idea — a surface that is measured.

**Neutral**

- **Background** (`oklch(98% 0.008 257)`): drafting paper. Tinted toward the accent hue, never
  pure white.
- **Surface** (`oklch(99.6% 0.005 257)`): raised card and node fill, a half-step above paper.
- **Border** (`oklch(90.5% 0.014 257)`): every hairline.

**Cyanotype — the dark theme**

A real blueprint is white linework on Prussian blue, so dark here inverts the value scale and
keeps the sheet. Same hue family, same grid, same single accent. It is **not** a terminal
theme, and the distinction is the whole point: relay lives in a dark terminal, and a page that
imitates its subject stops being a drawing *about* it.

| Role | Cyanotype |
|---|---|
| bg | `oklch(21% 0.055 258)` |
| surface | `oklch(25.5% 0.055 258)` |
| grid | `oklch(34% 0.045 258)` |
| border | `oklch(46% 0.042 258)` |
| fg | `oklch(96% 0.014 258)` |
| dim | `oklch(76% 0.032 258)` |
| ink | `oklch(72% 0.145 248)` |
| ink-deep | `oklch(83% 0.11 248)` |

Frontmatter holds the light values and stays normative; this table is the dark override, keyed
to the same role names.

Two asymmetries are deliberate. **Border sits heavier than its light counterpart** — 2.49:1
against paper's 1.25:1 — because a hairline that reads on paper disappears on ink. And
**ink-deep is lighter than ink**, not darker: on a dark sheet the hover state brightens, so the
token name describes its role, not its lightness.

The theme follows `prefers-color-scheme` and is overridable by a switch in the nav that
persists to `localStorage`. The switch is injected by script rather than authored into the
markup — without JS it could not work, and a dead control is worse than no control.

**The One Voice Rule.** Ink blue covers no more than 10% of any screen. It marks the outbound
leg, the links and one button. A page where the accent is everywhere has no accent.

**The Tinted Neutral Rule.** No neutral is pure grey. Every one carries chroma from the ink
hue, minimum 0.005. Pure greys read flat beside a saturated accent.

## Typography

**Display — Fira Code.** Headings, the wordmark, section bars, diagram labels, commands.
**Body — Fira Sans.** Running prose, lede, list content.
**Label/Mono — Fira Code.** Same family as display; labels are a weight and size register, not
a third face.

**Character.** Two cuts of one superfamily. The cohesion is the point: a drafting sheet labels
its drawing in the same hand it writes its notes. Fira Code in the heading slot keeps the page
reading as a document about software rather than a document about a company.

**Ligatures are off on every mono surface.** Fira Code renders `->` as an arrow and fuses `--`
by default. Both would put flags on the page that relay does not accept. `font-variant-ligatures:
none` plus `font-feature-settings: "liga" 0, "clig" 0, "calt" 0` on every element that sets the
mono family.

**Hierarchy**

- **Display** — Fira Code 500, `clamp(1.3rem, 1.02rem + 1.05vw, 1.8rem)`, line-height 1.22,
  tracking -0.022em. The single `h1`.
- **Headline** — Fira Code 600, `clamp(1.05rem, 0.95rem + 0.4vw, 1.3rem)`, tracking -0.015em,
  reversed out of the section bar. Names the section.
- **Title** — Fira Code 600, 0.9375rem, tracking -0.005em. Sub-heads inside a section.
- **Lede** — Fira Sans 400, `clamp(1rem, 0.96rem + 0.2vw, 1.125rem)`, line-height 1.6. The
  paragraph under the `h1`.
- **Body** — Fira Sans 400, 1rem, line-height 1.65, measure 65–75ch.
- **Body small** (`body-sm`) — Fira Sans 400, 0.9375rem, line-height 1.6. The herdr note and
  schedule descriptions.
- **Small** — Fira Sans 400, 0.875rem, line-height 1.55. Legend, node sub-labels, notes.
- **Caption** — Fira Sans 400, 0.8125rem, line-height 1.55. Edge captions in the stacked
  schematic.
- **Label** — Fira Code 500, 0.8125rem, line-height 1.4, tracking 0.02em. The primary button and
  schedule terms.
- **Label regular** (`label-regular`) — the label register at 400, line-height 1.65: nav links,
  the theme switch, the skip link, the colophon.
- **Wordmark** — Fira Code 600, 1.0625rem, tracking -0.015em. The nav's `relay`.
- **Name** — Fira Code 600, 1rem, tracking -0.01em. Node names in the stacked schematic.
- **Code** — Fira Code 400, 0.8125rem, line-height 1.8. Multi-line specimens. Inline code is
  `code-inline`, relative to the text it sits in, or `code-inline-sm` inside legend and
  schedule text.
- **Diagram** (`diagram-name`, `diagram-label`, `diagram-mono`) — node names in Fira Code 600,
  sub-labels in Fira Sans, paths, files and edge labels in Fira Code. Sizes are in the
  drawing's own units, not CSS pixels: they scale with the drawing, and the drawing is never
  rendered small enough to put a label under 12px on screen.

## Layout

**Macrostructure family: Map / Diagram.** One large spatial composition organises the page;
everything below it is annotation in the same drafting language, and none of it competes with
the drawing.

**Container.** `min(100% - 2 × gutter, 1240px)`, centred. Gutter is
`clamp(1.25rem, 4vw, 2.5rem)`.

**The first screen is the pitch and the diagram together.** At ≥900px the headline and the
install button share the first row of a 7/5 split, and the lede and the herdr dependency note
the second; below 900px the button follows the headline. The drawing sits directly beneath and
is sized to finish inside the fold, but never below the width at which its labels stay legible:
on a shorter screen it keeps that width and the page scrolls. Everything after that is reached
by scrolling — deliberately, so the first screen is one complete composition rather than a
headline with a diagram cut in half by the fold.

**The grid belongs to the diagram.** The 26px drafting grid is painted on the diagram's frame
only, never on `body`. Outside the drawing the page is plain paper. The grid is the surface the
machine is drawn on; using it as wallpaper makes it decoration.

**Sections are separated by inverted bars.** Each section opens with a solid `fg` bar spanning
the container, carrying the section name reversed out in `bg`. No hairline rules between
sections. The bar is the divider, the label and the rhythm in one element — a hairline on a
grid background reads as another grid line.

**Breakpoints.** 900px is the only structural one: below it the first-screen split collapses to
a single column and the diagram becomes a vertical node schematic. 1000px is the diagram's own
threshold — below it the drawn SVG is not rendered at all and the node list *is* the diagram.

**Rhythm.** Section bar, then content, then `xl` space before the next bar. More space above a
bar than below it.

**Nav — N9 edge-aligned minimal, sticky.** Wordmark left, two lowercase links right, pinned to
the top. It carries a `bg` fill and a hairline bottom at all times, stuck or not, so content
never runs under transparent chrome.

**Footer — Ft2 inline single line.** One rule, one row, colophon facts. No column index.

## Elevation & Depth

**Tonal, not cast.** There are no shadows on this page. Depth comes from three values of paper
— `bg`, `surface`, `grid` — and from hairline borders. A drafting sheet has no shadows because
nothing on it is floating.

**Shadow Vocabulary**

- `none` — every card, node, bar and button.
- The one permitted exception is a focus ring, which is an outline, not a shadow:
  `outline: 2px solid var(--ink); outline-offset: 3px`.

**The No Ghost Card Rule.** Elevation is declared once. A hairline border or a tonal fill,
never both plus a shadow.

## Shapes

**Radius is zero, everywhere.** Buttons, cards, nodes, bars, inputs, code blocks. There is no
radius scale because there is no radius. A drawn system has corners.

**Borders are 1px hairlines** in `border`, or 1.5px in `fg` for diagram node outlines, which
are drawn objects rather than UI chrome.

**The recurring silhouette is the labelled rectangle** — the diagram node, the section bar, the
code specimen and the colophon row are all the same shape at different scales.

## Components

**Section bar.** Full container width, `fg` fill, `bg` text, mono 600, squared. Carries the
section name. It is the only reversed surface on the page and it is what gives the page its
rhythm; nothing else may be reversed out without a reason recorded here.

**Diagram node.** `surface` fill, 1.5px `fg` outline, mono name, sans sub-label. Hovering or
focusing one highlights its edges. Each node is a real link to the section that explains it.

**Diagram edges.** Solid `ink` for outbound (the planner calls the CLI and it returns inside the
same turn). Dashed `dim` for inbound (the report lands after the turn ended). The legend states
both. This distinction is the page's whole argument and the two edge styles must never be
unified.

**Button — primary.** `ink` fill, `bg` text, squared, mono label. One per page.

**Code specimen.** `grid` fill for inline, hairline-bordered `surface` block for multi-line.
Horizontal scroll inside the block, never on the page.

**Link.** `ink`, 1px underline at `0.18em` offset, going `ink-deep` on hover.

**Motion.** One authored moment: the diagram's nodes ink on one at a time as it enters the
viewport, capped at five. Nothing else on the page animates in. Under
`prefers-reduced-motion: reduce` the diagram renders complete immediately — the reduced state is
the finished drawing, not a slower one.

## Do's and Don'ts

- **Do** keep the drafting grid inside the diagram frame. **Don't** paint it on `body`.
- **Do** separate sections with the inverted bar. **Don't** add hairline rules between sections
  — on a measured surface a hairline reads as another grid line.
- **Do** let the first screen hold the pitch and the drawing together as one composition.
  **Don't** let the diagram be sliced by the fold.
- **Do** disable ligatures anywhere the mono face is set. **Don't** ship a page about a CLI that
  renders `--flag` as `–flag`.
- **Do** keep radius at zero. **Don't** introduce a pill or a rounded card.
- **Do** keep the accent under 10% of any screen. **Don't** fill a section with ink blue.
- **Do** state relay's real commands, formats and flags. **Don't** author invented session
  output — and when a demonstration is unavoidable, label it.
- **Don't** add a shadow. Depth here is tonal.
- **Don't** reverse anything out of `fg` except a section bar.
- **Do** keep the dark theme a cyanotype. **Don't** let it drift toward a terminal — no green,
  no near-black, no zero-chroma greys. If dark mode stops reading as a blueprint, the direction
  has been lost.
- **Do** define every colour as a token in both themes. **Don't** hardcode a colour value
  anywhere; a literal `rgba()` in a rule is a value that cannot follow the theme.
