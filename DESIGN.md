---
name: trackside
description: The national public timetable, made live, set with restraint.
colors:
  timetable-blue: "#1f4fb8"
  timetable-blue-deep: "#193f94"
  timetable-blue-text: "#1f4fb8"
  timetable-blue-wash: "#eef2fa"
  on-blue: "#ffffff"
  signal-green: "#1a8a4f"
  signal-amber: "#e2a01c"
  signal-red: "#cc3b24"
  signal-red-ink: "#b2321d"
  lamp-off: "#c2c8d0"
  unlit-mark: "#9aa3ae"
  flap-leaf: "#1b1f25"
  flap-ink: "#f3f4f6"
  paper: "#f6f7f9"
  sheet: "#ffffff"
  well: "#eceff3"
  ink: "#0f1419"
  ink-2: "#3a434e"
  muted: "#5d6876"
  rule: "#e0e4ea"
  rule-strong: "#c3cad4"
typography:
  display:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "clamp(2.4rem, 5.2vw, 3.9rem)"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: "-0.03em"
    fontVariation: "'wdth' 94"
  headline:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "clamp(1.9rem, 3.6vw, 2.6rem)"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "-0.02em"
    fontVariation: "'wdth' 94"
  title:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "1.02rem"
    fontWeight: 600
    lineHeight: 1.25
    fontVariation: "'wdth' 94"
  figure:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "1.3rem"
    fontWeight: 600
    lineHeight: 1
    letterSpacing: "-0.01em"
    fontFeature: "tnum"
    fontVariation: "'wdth' 84"
  body:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.5
    fontFeature: "tnum"
  label:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: 1.5
  code:
    fontFamily: "Archivo Variable, Archivo, system-ui, sans-serif"
    fontSize: "0.8rem"
    fontWeight: 600
    letterSpacing: "0.08em"
    fontVariation: "'wdth' 84"
rounded:
  tile: "3px"
  flag: "4px"
  plate: "5px"
  control: "6px"
  menu: "8px"
  panel: "10px"
  lamp: "50%"
spacing:
  cell: "2px"
  tight: "0.4rem"
  snug: "0.6rem"
  base: "1rem"
  column: "1.25rem"
  section: "1.5rem"
  gutter: "clamp(1rem, 3vw, 2rem)"
  page-end: "4rem"
components:
  button-primary:
    backgroundColor: "{colors.timetable-blue}"
    textColor: "{colors.on-blue}"
    rounded: "{rounded.control}"
    padding: "0 1rem"
    height: "2.5rem"
  button-primary-hover:
    backgroundColor: "{colors.timetable-blue-deep}"
  button-quiet:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 1rem"
    height: "2.5rem"
  button-quiet-hover:
    backgroundColor: "{colors.well}"
  input-field:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 0.75rem"
    height: "2.5rem"
  select-field:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 2rem 0 0.75rem"
    height: "2.5rem"
  segmented:
    backgroundColor: "{colors.well}"
    textColor: "{colors.muted}"
    rounded: "{rounded.menu}"
    padding: "3px"
    height: "2.5rem"
  segmented-selected:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
  platform-plate:
    backgroundColor: "{colors.timetable-blue}"
    textColor: "{colors.on-blue}"
    typography: "{typography.figure}"
    rounded: "{rounded.plate}"
    padding: "0 0.3rem"
    size: "1.9rem"
  platform-plate-changed:
    backgroundColor: "{colors.signal-amber}"
    textColor: "{colors.ink}"
    rounded: "{rounded.plate}"
  platform-plate-lead:
    size: "2.4rem"
  flap-tile:
    backgroundColor: "{colors.flap-leaf}"
    textColor: "{colors.flap-ink}"
    typography: "{typography.figure}"
    rounded: "{rounded.tile}"
  signal-lamp:
    backgroundColor: "{colors.lamp-off}"
    rounded: "{rounded.lamp}"
    size: "0.5rem"
  stop-flag:
    backgroundColor: "{colors.timetable-blue-wash}"
    textColor: "{colors.timetable-blue-text}"
    rounded: "{rounded.flag}"
    padding: "0.05rem 0.45rem"
  floating-panel:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.panel}"
    padding: "1rem"
---

# Design System: trackside

## Overview

**Creative North Star: "The Public Timetable"**

trackside reads like a page of the national public timetable, made live. A cool neutral ground, a white shell bar and near-black ink carry everything; hairline rules divide rows instead of boxes; one family, Archivo, does all the talking, narrowed for figures and names. Timetable blue is held back as an accent for the things a rider acts on: a confirmed platform, the route line, the current selection, focus, and the primary button.

The mood is restrained, precise and calm. The first, louder rendition of this world used full-strength colour fields and read as tacky to the user; this one withdraws them. What remains is the railway's own furniture at small scale: a signal lamp for status, a plate for the platform, a line diagram for the route, and split-flap leaves that flip when a figure changes. The motion is the user's own request and is pinned; it is the one animated idea, and it fires only on change.

Density is board density. A rider should find their train, platform and lateness in one glance down the time column, while an enthusiast reads headcode and operator on the same row. The rejected reference is the dark dashboard of identical list rows. Light and dark themes are both first-class, switched by `prefers-color-scheme`.

**Key Characteristics:**
- Cool neutral paper, white sheet, near-black ink; hairlines, not boxes.
- Timetable blue as a rationed accent: plates, route line, selection, focus, primary action.
- Archivo variable throughout: 84% width for figures, 94% for names, normal width for prose; tabular figures everywhere.
- Status said by small marks (signal lamps, a changed plate, a notice dot), always alongside words.
- Split-flap motion on changed figures; the next train's time sits on dark split leaves.
- The next train leads: a larger row at the top of the board.

## Colors

A cool grey-white ground and near-black ink, with one rationed blue and three signal colours kept to small marks.

### Primary
- **Timetable Blue** (`timetable-blue`): the solid fill of a confirmed platform plate, the route line on the train page, the active nav underline (2px), primary buttons, the selected train ring on the map, notice dots of the lowest severity, and checkbox accent. In dark mode it lifts to a brighter blue (see sidecar).
- **Timetable Blue Deep** (`timetable-blue-deep`): primary button hover only.
- **Timetable Blue Text** (`timetable-blue-text`): blue used as text: link buttons, the running-trains link on Home, station index hover, route stop flags. Equal to the accent in light mode; a pale periwinkle in dark mode so small text stays legible.
- **Timetable Wash** (`timetable-blue-wash`): the quiet tint behind the active search result, map match hover, route stop flags and the Delay Repay claim line.

### Secondary
- **Signal Green** (`signal-green`): the lamp for a train on time; the map mark fill for the same.
- **Signal Amber** (`signal-amber`): the lamp for slight lateness (one lamp) and moderate lateness (two stacked lamps, "double yellow"); the changed-platform plate; the default notice dot.
- **Signal Red** (`signal-red`): the lamp for very late or cancelled; the cancelled stop node ring; the severe notice dot.
- **Signal Red Ink** (`signal-red-ink`): red as text: delay and cancellation reasons, error notes, cancelled live times. Shifted to a light coral in dark mode.
- **Lamp Off** (`lamp-off`) and **Unlit Mark** (`unlit-mark`): the unlit lamp and the map mark for a train whose state is unknown.

### Tertiary
- **Flap Leaf** (`flap-leaf`) and **Flap Ink** (`flap-ink`): the dark split leaves the next train's time and lead platform sit on, with a 1px dark seam across the middle. The only dark slab in the light theme, and it is figure-sized.

### Neutral
- **Paper** (`paper`): the page ground.
- **Sheet** (`sheet`): the shell bar, inputs, menus, panels, and the board row hover.
- **Well** (`well`): the segmented control track, quiet button hover, map legend marks.
- **Ink** (`ink`): headings, figures, body text.
- **Ink 2** (`ink-2`): secondary text: ledes, notices, journey times, headcodes, "was N" notes.
- **Muted** (`muted`): column heads, sub-lines, labels, CRS codes, refresh line.
- **Rule** (`rule`): row hairlines and panel borders.
- **Rule Strong** (`rule-strong`): the hairline under column heads, input strokes, outline plates, journey rules, dot leaders.

### Named Rules
**The Rationed Blue Rule.** Blue goes only where a rider acts or must trust a fact: confirmed plates, the route line, the active state, focus and the primary button. No surface larger than a plate or a button is ever filled with it.

**The Small Marks Rule.** Status colour lives in marks a few pixels across: a lamp, a notice dot, a changed plate. It never fills a row, a band or a block of text background, and every colour mark has words beside it (the aspect label, "was 4", the notice label).

## Typography

**Display Font:** Archivo Variable (with Archivo, system-ui, sans-serif), loaded from `@fontsource-variable/archivo` with the width axis.
**Body Font:** the same family at normal width.

**Character:** One grotesque in three widths. Narrowed figures make the time column read like a printed timetable; slightly narrowed names hold long destinations on one line; prose stays at full width and comfortable.

### Hierarchy
- **Display** (700, `clamp(2.4rem, 5.2vw, 3.9rem)`, line-height 1, -0.03em): the Home heading only, up to 14em wide.
- **Headline** (700, `clamp(1.9rem, 3.6vw, 2.6rem)`, 1.1, -0.02em): the station name on a board, the train on its page (slightly smaller there), with the CRS code beside it at 0.45em in muted.
- **Title** (600, 1.02rem, 1.25, 94% width): destinations on board rows and stop names on the route (1.05rem). The lead row's destination rises to 1.4rem at weight 650.
- **Figure** (600, 1.3rem, line-height 1, 84% width, tabular): booked times in the time column; 1.8rem on the lead row, 1.2rem on phones. The same width and weight set platform numbers, journey end times and route times (1.02rem).
- **Body** (400, 15px, 1.5; 16px under 640px): prose, run status, notices (0.93rem). Ledes run 1.05rem in ink-2, max 42em.
- **Label** (500, 0.75rem): column heads stated once above the board and route; field labels at 0.8rem; route time labels at 0.72rem.
- **Code** (600, 0.8rem, +0.08em, 84% width): CRS codes and headcodes (headcodes at 0.9em, +0.06em).

### Named Rules
**The Three Widths Rule.** Figures at 84% width, names at 94%, prose at 100%. A new number or proper name picks its width by what it is, not by where it sits.

**The Tabular Rule.** `font-variant-numeric: tabular-nums` is set on the body, so every column of times aligns digit for digit. Never switch it off.

## Layout

Content sits in a centred column, max 68rem (54rem on the train page), with side gutters of `clamp(1rem, 3vw, 2rem)` and 4rem below. The shell bar is a single 3.5rem row: wordmark, nav, then search pushed right; under 760px it wraps and the search takes a full-width third line.

The board is a five-column grid (time, place, journey, platform, status) with a 1.25rem gap, stated once in quiet column heads over a strong hairline. Rows are at least 3.75rem tall and divided by hairlines. The first row, the next train, leads: wider time column (7.75rem), 5.5rem tall, larger figures and plate. Under 760px rows fold to a two-line grid (time down the left; place and plate on top; journey and status below), column heads hide, and the lead row restacks to time and plate, then place, then journey and status.

The train page is a four-column route grid (times right-aligned, line, stop, platform), narrowing to 5.2rem and 1.3rem tracks under 640px where the arr/dep labels drop.

The map is full-bleed with one floating panel at top left (22rem, half height on phones).

### Named Rules
**The Journey Length Rule.** On a board, each row's journey rule is as long as its journey: its length is the journey time over the longest journey on the board (never less than an hour, never shorter than 8% of the track). Length means duration and nothing else.

**The Said Once Rule.** Column heads and the platform key (solid plate is confirmed, outline is booked) are stated once per board, never repeated per row.

## Elevation & Depth

Flat by default. Depth comes from the paper/sheet/well tonal steps and hairlines; shadows appear only on things that genuinely sit above the page.

### Shadow Vocabulary
- **Raise** (`box-shadow: 0 1px 2px rgb(15 20 25 / 0.08), 0 1px 1px rgb(15 20 25 / 0.04)`): the selected segment and the Home search field. A slight lift, not a card.
- **Float** (`box-shadow: 0 12px 32px rgb(15 20 25 / 0.14), 0 2px 6px rgb(15 20 25 / 0.06)`): search result menus, the map panel, the skip link on focus.

Dark mode deepens both (see sidecar).

### Named Rules
**The Hairline Rule.** Rows, notices, route stops and definition lists are divided by 1px rules, never wrapped in cards. Boxes are reserved for a form (Delay Repay) and things that float (menus, the map panel).

## Shapes

Gently squared corners, smaller as things get smaller: 3px on flap tiles and the plate key, 4px on stop flags and the focus ring, 5px on platform plates and menu rows, 6px on buttons and inputs, 8px on the segmented track, menus and the large Home search, 10px on the map panel and Delay Repay form. Lamps, notice dots, route nodes and the journey rule's end dot are circles. The route line is a 3px bar with 2px rounding. No pills, no sharp 0px corners.

## Components

### Buttons
Plain and firm.
- **Shape:** gently squared (6px), 2.5rem tall, 1rem side padding, weight 600.
- **Primary:** timetable blue with white text; hover deepens to blue-deep over 120ms ease-out. Disabled drops to 60% opacity with a progress cursor.
- **Quiet:** sheet ground, ink text, a 1px rule-strong inset stroke; hover fills with well.
- **Link button:** blue text, underlined at 3px offset, no box.

### Segmented control
- **Style:** a well-coloured track (8px, 3px padding, 2.5rem) holding transparent muted buttons (6px).
- **State:** the selected segment is sheet with ink text, weight 600 and the raise shadow. Used for Departures/Arrivals.

### Inputs / Fields
- **Style:** 2.5rem, 1px rule-strong stroke, 6px, sheet ground; labels above in muted 0.8rem. The shell search sits on paper with a lighter rule. The Home search is the large variant (3.25rem, 8px, 1.1rem, raise shadow).
- **Hover:** the stroke darkens toward ink.
- **Focus:** stroke turns to the focus colour with a 3px translucent focus halo. Everything else focuses with a 2px solid outline at 2px offset.
- **Platform filter (multi-select):** a field-styled button (2.5rem, 1px rule-strong stroke, 6px, sheet ground, weight 500, the drawn muted chevron) reading "All platforms", "Platform 6", "Platforms 4, 6" or "5 platforms"; it takes a 1px blue inset ring when a choice is set. It opens a floating panel (sheet, 1px rule, 8px, float shadow, 0.75rem padding, up to 19rem wide) with a muted "N selected" line and a "Show all" link, then a grid of platform toggle tiles (2.4rem high, 1px rule-strong, 6px, figure weight 600). A selected tile fills timetable blue with white figures. Each tile is a real checkbox underneath, labelled "Platform N" for screen readers. Escape or a click outside closes it. It spans the full width in the two-column phone controls grid. Moved trains still list under a selected old platform with their "was N" note.
- **Time picker ("From"):** a quiet field-styled button with the same drawn chevron, opening the native picker.

### Navigation
- **Style:** a white shell bar with a bottom hairline; wordmark at 1.15rem/700/94% width beside a small two-lamp signal-head mark.
- **States:** links in muted, 0.93rem, 500; hover goes to ink; the active link is ink with a 2px timetable-blue underline drawn at the bar's bottom edge.

### Search results
- **Style:** a floating menu (sheet, rule border, 8px, float shadow) of rows with name left and code right in the code style; the active or hovered row takes the blue wash.

### Signal lamp (signature)
A small signal head that states a train's running status. One 0.5rem circle, coloured green, amber or red, or two stacked 0.4rem amber lamps for double yellow; unlit grey when unknown. Plain dots: no halo, no glow. It always sits beside the status words and carries an accessible label. Map train marks use the same aspect colours (with a dark bar splitting double yellow).

### Platform plate (signature)
A platform number on a 1.9rem square plate (2.4rem on the lead row), figure type at 84% width.
- **Confirmed:** solid timetable blue, white figure.
- **Booked / not yet announced:** transparent with a 1px rule-strong inset outline and ink figure.
- **Changed:** amber with a dark figure, and "was N" written in ink-2 beneath it; the change is never told by amber alone.
- **Unknown:** an empty outline in the lighter rule.

### Split-flap figures (signature, pinned)
Changed figures flip. Inline (status times, platform numbers) each changed character turns about its middle (150ms), stepping through two or three stand-in characters of the same kind, left to right with a 50ms stagger. On tiles (the lead row's time and plate) each character sits on its own dark leaf (flap-leaf, 3px, 2px apart) with a 1px seam across the middle; a flip drops the old top half over the hinge (75ms ease-in, brightening) then the new bottom half into place (75ms ease-out, from dark). Separators sit off-leaf in muted. Screen readers get the plain value; reduced motion shows the final value at once.

**The Pinned Flap Rule.** Only a figure whose value has changed flips, plus the lead row's introduction. Nothing else in the interface animates beyond 120ms colour transitions.

### Journey line
On each board row, a 1px rule-strong line ending in a 5px ink-2 dot, then the arrival (or departure) time in figure type. On arrivals boards it runs right to left. Cancelled journeys fade to 35%.

### Route line diagram (signature)
The train page draws the route as a station line: a 3px blue bar at 20% strength ahead of the train and full strength behind it. Calling points are 0.75rem rings (filled once passed); passing points are 0.5rem rings with muted 0.93rem names; the train's current position is a 1.1rem ink node ringed in paper then blue. Cancelled stops ring in red with struck names and times. Stop flags (origin, destination, request) are small wash-tinted tags in blue text.

### Notices
A hairline list. Each notice has a 6rem label column (0.8rem, 600, ink) led by a 0.45rem dot: amber by default, blue for the lowest severity, red for the highest. Text in ink-2 at 0.93rem.

## Do's and Don'ts

### Do:
- **Do** keep blue to confirmed plates, the route line, active and selected states, focus and the primary button.
- **Do** say every status in words beside its lamp, dot or plate; write "was N" under a changed plate.
- **Do** set times and platforms in the figure style (600, 84% width, tabular) and names at 94% width.
- **Do** divide rows with 1px hairlines (rule, or rule-strong under heads) and state column heads once.
- **Do** make a new live figure a `Flap` so a change announces itself, and honour reduced motion.
- **Do** give every new colour a dark-mode value alongside the light one under `prefers-color-scheme`.

### Don't:
- **Don't** fill bands, rows, headers or panels with full-strength colour; the user withdrew colour fields as tacky. Solid colour stops at the size of a plate or a button.
- **Don't** wrap board rows, route stops or notices in cards.
- **Don't** animate anything except changed figures; no entrance choreography, no hover lifts.
- **Don't** add halos or glows to signal lamps; they are plain dots.
- **Don't** use any second typeface; Archivo carries display, figures and prose.
- **Don't** reintroduce the replaced navy-and-Barlow Platform Sign world.
