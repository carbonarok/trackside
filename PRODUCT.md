# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

The website serves four audiences, with no single one ranked first:

- **The self-hoster:** the person who runs an instance and uses it to check their own trains, as a personal replacement for a paid train-times service.
- **Public riders:** ordinary passengers checking a departure, seeing whether their train is late, or working out a Delay Repay claim. They only meet trackside if someone runs a public instance.
- **Rail enthusiasts:** people who care about headcodes, train IDs, berths, associations (divide, join, next working), working times and punctuality history.
- **Developers evaluating it:** people looking at what the data and the API can do before or while they run their own copy.

The JSON API (native and Realtime Trains v1 compatible) serves client developers; the website is the human face of the same data.

## Product Purpose

trackside is an open-source server for UK train times. It loads the GB rail timetable, follows trains live, and serves departure boards and service details as JSON and as a built-in website. It exists because the author's train API started charging: anyone should be able to run their own copy for free, with their own feed credentials, using only public open data.

Success means a person can run a single binary plus Postgres and get boards, train pages, a live map and Delay Repay answers that match what station screens show, without paying anyone.

## Positioning

**Free, open and yours.** trackside is built only on public open-data feeds (Network Rail SCHEDULE, CORPUS, TRUST, VSTP, TD/SMART; National Rail Darwin), is AGPL-3.0 licensed, costs nothing to run, and offers a drop-in Realtime Trains v1 compatible API. National Rail, Realtime Trains and operator apps can't truthfully say any of that. The depth of the live detail (Train Describer positions, associations, history) supports this, but openness is the point.

## Operating Context

- Each instance is self-hosted: one static Go binary with the React website embedded, plus Postgres. Default address is `http://localhost:8080/`; API docs are rendered at `/docs`.
- Website routes: home (search and busy stations), station board `/station/:code`, train page `/train/:uid/:date` with the route drawn as a line diagram, live map `/map`, Delay Repay checker `/delay-repay`.
- Search accepts station names or CRS codes, headcodes and train IDs.
- Data quality depends on which feeds the operator has configured. With only a Network Rail account, times are trackside's own estimates from reported delays; with Darwin, forecasts and platforms match station screens. Map positions between stations are estimates.
- Darwin may suppress live platforms, in which case only the booked platform is shown.
- Users check trains on the move, at stations and at home; phone use is expected.

## Capabilities and Constraints

- **Status: early.** Feeds are tested against sample data in the real message formats but have not run against live feeds for long. The README warns: expect bugs and occasionally wrong times; don't rely on it for connections you can't afford to miss; check official sources.
- Features: full GB timetable with overlays and STP cancellations, short-notice VSTP trains, associations, TRUST live running, Darwin forecasts and delay/cancellation reasons, station messages, Train Describer berth positions with "approaching" and "at platform" status, a live train map coloured by lateness (also as GeoJSON), up to 400 days of history with punctuality per station and train, and a Delay Repay checker (lateness, compensation band, next train if cancelled).
- Map tiles default to OpenStreetMap, which is fine for personal use; a public instance must use its own tiles or a provider.
- Terminology to preserve: board, service, headcode, train ID / UID, CRS, TIPLOC, berth, platform (booked vs live), association, Delay Repay, punctuality.
- **Open decision:** whether a public, shared instance will exist. Until decided, the website must work for the self-hoster and must not assume visitors already know the context.

## Brand Commitments

- Name is lower-case: **trackside**.
- Voice, from the README and existing UI copy: plain British English, direct and candid, honest about limits ("Built on vibes", "Don't rely on it for anything important"). Short sentences, no hype.
- The existing website already has a visual identity in `web/src/styles.css`. Product init does not record or change it.

## Evidence on Hand

- README.md: feature list, feed table, setup and troubleshooting, roadmap.
- Real live data from whichever feeds an instance is connected to; history accumulates per instance.
- `web/public/favicon.svg`.
- LICENSE (AGPL-3.0).
- No testimonials, user counts, public instance, press or benchmarks exist beyond the README's own import figures (about 1 GB timetable in about a minute under 100 MB). Do not invent any.

## Product Principles

1. **Open data, openly.** Every number traces back to a public feed. Show where data comes from and when it is an estimate.
2. **Honest about uncertainty.** Distinguish booked, estimated, forecast and actual times; never present an estimate as fact, and keep the early-status caveat visible where it matters.
3. **Serve the rider and the enthusiast at once.** The common answer (is my train on time, which platform) comes first, with the deeper detail (headcodes, berths, associations, history) available without getting in the way.
4. **Runs anywhere, costs nothing.** Nothing in the website may require a paid service or extra hosting beyond the single binary.

## Accessibility & Inclusion

No formal standard has been set. Existing UI already uses ARIA labels for signals and platforms, an accessible combobox search, visible focus rings and reduced-motion handling. Keep that baseline. Lateness colouring must never be the only way status is conveyed.
