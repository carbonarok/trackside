---
version: 1
slug: "web-src-pages-board-tsx"
primary_target: "web/src/pages/Board.tsx"
related_targets: ["web/src/components/Layout.tsx","web/src/pages/Home.tsx","web/src/pages/Service.tsx","web/src/pages/MapPage.tsx","web/src/pages/DelayRepayPage.tsx"]
---

# Station board (and the site shell it sets)

Scope: the station board `/station/:code` leads a full replacement of the web UI's visual world; Home, Train page, Live map and Delay Repay follow it. Mode: Operate.

Audience and job: riders checking a departure (time, platform, is it late) on a phone or at a desk; enthusiasts reading headcodes, operators and history. Constraints: live data refreshes every 30 s; status never by colour alone; light and dark follow the OS; reduced motion respected. Kept from the old site by user choice: signal-lamp status, route line diagram, platform and headcode marks, redrawn in this world.

## Direction contract

THESIS: The board is a page of the national public timetable, made live. It refuses the dark dashboard of identical list rows.

OWN-WORLD: Restrained, by the user's direction after the first build ("make it look more professional, it looks tacky"). A cool neutral ground with a white shell and hairline rules; near-black ink. Timetable blue is an accent only: confirmed platform plates, the route line, selection, focus, primary buttons. No coloured slabs. Archivo, slightly condensed and semibold for figures and names, normal for prose. Hairlines, not boxes; 5-10px radii on controls. The signal lamp (a dot, two for double yellow) is the only status colour. Split-flap motion is user-pinned: changed figures flip, and the next train's time sits on small dark split leaves.

STORY: The rider finds their train, platform and lateness in one glance down the time column; the enthusiast reads headcode and operator on the same row; any change on refresh announces itself by flipping.

FIRST VIEWPORT: A white shell bar (wordmark, Live map, Delay Repay, search). The station name as a plain large heading with its code and time window in muted text. The controls row (segmented Departures/Arrivals, Calling at, From). Notices. Quiet column heads stated once over a hairline. The next train larger, its time on flap leaves. Then rows, each with a journey line whose length is its journey time.

FORM: The Public Timetable (BR timetable book), position 3 on the ordered list; seed e77ae610; raised by journey-length rule, next-train lead, hairline discipline, nothing labelled twice; split-flap motion pinned by the user; full-strength colour fields withdrawn at the user's request for a professional, restrained finish.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
