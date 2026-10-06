# trackside

An open-source server for UK train times. It loads the GB rail timetable,
follows trains live, and serves departure boards and service details as JSON.

It is built only from public open data feeds: Network Rail's timetable, train
movements and Train Describer, and National Rail's Darwin. Anyone can run their
own copy for free, with their own feed credentials.

> [!WARNING]
> **Built on vibes.** trackside was written quickly, with a lot of help from
> an AI coding assistant, by someone annoyed that their train API started
> charging. It has tests, and the data formats were checked against the
> published documentation, but nobody has yet run it against the live feeds
> for long. Expect bugs, odd edge cases and times that are occasionally
> wrong. **Don't rely on it for anything important**, such as making a
> connection you can't afford to miss, and check against official sources
> (National Rail Enquiries, station screens, operator apps). Bug reports and
> pull requests are very welcome.

> **Status: early.** Every feed below is implemented and tested end to end
> against sample data in the real message formats, but has not yet run against
> the live feeds. See [Roadmap](#roadmap).

## Features

- **Full GB timetable** from Network Rail's SCHEDULE feed. It handles
  overlays, STP cancellations and trains that run past midnight.
- **Short-notice trains** from the VSTP feed, applied as they arrive.
- **Live running** from TRUST: activations, actual times, platform changes,
  cancellations, reinstatements, and changes of origin or identity. Delays are
  carried forward to estimate times at later stops.
- **Darwin forecasts** from National Rail: estimated times, live platforms
  (hidden while Darwin suppresses them), cancellations, and readable delay and
  cancellation reasons. Darwin's estimates replace trackside's own wherever it
  has them.
- **Train Describer positions:** signalling berth steps are mapped to arrivals
  and departures with the SMART data, giving times ahead of TRUST, the train's
  current berth, and "at platform" status.
- **Two ways to connect:** each Network Rail feed can come from Network Rail's
  own STOMP service or from the Rail Data Marketplace's Kafka. Darwin comes
  from the Rail Data Marketplace.
- **Native JSON API** with ISO 8601 times and clear field names.
- **Compatibility API** that copies the legacy Realtime Trains v1 JSON shape
  (`/api/v1/json/...`). An existing client can switch over by changing only its
  base URL.
- **Easy to run.** It is a single static binary plus Postgres. The full
  timetable (about 1 GB of JSON) imports in about a minute using under 100 MB
  of memory.

## Setup

Setting up has three parts: get access to the data, run trackside, and do
the first imports. Getting access takes the longest, because both data
providers approve accounts by hand. Request them first.

### Which feeds you need

| Feed | What it gives you | Where it comes from | Needed? |
|---|---|---|---|
| SCHEDULE | The timetable | Network Rail | **Yes** |
| CORPUS | Station and location names and codes | Network Rail | **Yes** |
| TRUST (Train Movements) | Live actual times, cancellations | Network Rail | Recommended |
| VSTP | Trains added at short notice | Network Rail | Recommended |
| Darwin Push Port | Forecasts, live platforms, delay reasons | Rail Data Marketplace | Recommended |
| Darwin reference data | Proper station names, reason texts | Rail Data Marketplace | Optional |
| TD (Train Describer) + SMART | Earlier times, train positions | Network Rail | Optional |

With only a Network Rail account you get the full timetable and live running,
with trackside estimating times from reported delays. Adding Darwin gives you
the same forecasts and platforms that station screens show.

### 1. Get a Network Rail open data account

1. **Register at [publicdatafeeds.networkrail.co.uk](https://publicdatafeeds.networkrail.co.uk/).**
   The [Open Rail Data wiki](https://wiki.openraildata.com/index.php/About_the_Network_Rail_feeds)
   documents the process. The wiki's own "Request account" link only gives
   you a login for editing the wiki, not data access.
2. **Set your password.** You'll get a confirmation email. Log in and change
   your password. It must be at least 8 characters and include upper case,
   lower case, a number and a special character. The site may show an
   unhelpful error if it doesn't meet these rules.
3. **Wait for activation.** Network Rail emails you when the account is
   active. Until then every request is refused (see
   [Troubleshooting](#troubleshooting)). The platform has a cap of 1,000
   users, first come first served.
4. **Subscribe to the feeds.** Once you're active, go to **My Feeds** and
   subscribe to:
   - **Train Movements**: all TOCs (`TRAIN_MVT_ALL_TOC`)
   - **VSTP**: all TOCs (`VSTP_ALL`)
   - **SCHEDULE**: all TOCs, daily full and update files
   - **TD**: all signalling areas (`TD_ALL_SIG_AREA`). Optional.
5. **Add your credentials to `.env`:**

   ```bash
   NR_USERNAME=you@example.com      # the email you registered with
   NR_PASSWORD=...
   NR_CLIENT_ID=trackside-yourname  # any unique name; it labels your subscriptions
   ```

Network Rail asks every client to:

- use one account
- connect once
- stop on authentication errors
- back off when reconnecting
- use durable subscriptions

trackside does all of these. All live feeds share one connection, and the
subscriptions are durable, so messages queue on the broker while trackside
restarts. Reconnects back off exponentially. If the credentials are rejected,
trackside stops retrying until you restart it.

### 2. Get a Rail Data Marketplace account (for Darwin)

1. **Register at [raildata.org.uk](https://raildata.org.uk).** New accounts
   and some products are approved manually, so this can take a few days.
2. **Subscribe to Darwin.** Find **Darwin Real Time Train Information** (the
   Darwin Push Port) and subscribe.
3. **Copy the connection details.** Open the product's **Pub/Sub** tab and
   copy the consumer key, consumer secret and consumer group into `.env`:

   ```bash
   RDM_DARWIN_USERNAME=<consumer key>
   RDM_DARWIN_PASSWORD=<consumer secret>
   RDM_DARWIN_GROUP=<consumer group, e.g. SC-1234abcd-...>
   ```

   The Rail Data Marketplace issues exactly one consumer group per product,
   and you must use that one. Any other group ID is refused. If the bootstrap
   server shown differs from the default, set `RDM_BOOTSTRAP` too.
4. **Optional: reference data.** Subscribe to **Darwin Timetable Files** to
   get proper station names ("London Waterloo" rather than "LONDON
   WATERLOO"), operator names and reason texts. You can't download these
   files on demand: you set up delivery to your own SFTP, S3, Azure or GCS
   storage. Then load the latest `*_ref_v4.xml.gz` file:

   ```bash
   trackside import-darwin-ref 20261006020500_ref_v4.xml.gz
   ```

**Network Rail feeds through the Rail Data Marketplace.** The marketplace
also carries Network Rail's feeds over Kafka. This is useful if Network
Rail's platform is full. Subscribe to the Train Movements, VSTP or TD
product and set `RDM_TRUST_*`, `RDM_VSTP_*` or `RDM_TD_*` in the same way.
Any feed configured like this is read from Kafka instead of Network Rail.

The marketplace delivers SCHEDULE, CORPUS and SMART as files to your own
storage too. Load them with `import-schedule FILE`, `import-corpus FILE` and
`import-smart FILE`.

### 3. Run it

With Docker:

```bash
cp .env.example .env        # then fill in the credentials from steps 1 and 2
docker compose up -d        # starts Postgres and the server on :8080
docker compose run --rm trackside import-corpus
docker compose run --rm trackside import-schedule
docker compose run --rm trackside import-smart     # optional, for Train Describer
```

Without Docker (Go 1.25+ and Postgres 14+):

```bash
go build ./cmd/trackside
createdb trackside
cp .env.example .env        # set DATABASE_URL and the credentials
./trackside import-corpus
./trackside import-schedule
./trackside import-smart    # optional
./trackside serve
```

**First imports:**

- `import-corpus` and `import-smart` take a few seconds.
- `import-schedule` downloads the full timetable (about 100 MB compressed,
  1 GB of JSON) and loads it in a minute or two.
- Each import command also accepts a local file (`.json` or `.json.gz`)
  instead of downloading.

**What happens automatically once `serve` is running:**

- The daily timetable update is applied each morning, from 06:00. Network
  Rail publishes it around then.
- VSTP trains are added as they arrive.
- Services are re-resolved hourly, so the 7-day window keeps rolling forward.
- TRUST, Darwin and TD are followed live.

**Check it works:**

```bash
curl 'localhost:8080/v1/locations/CLJ/departures'
```

The logs show `subscribed topic=...` for each Network Rail feed and
`consuming topic=...` for each Kafka feed.

### Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `401 Unauthorized` from an import, or `AMQ339009 Exception getting session` in the logs | Network Rail is refusing the login. Check, in order: the account has been activated (you get an email); you're subscribed to the feed under **My Feeds**; and `NR_USERNAME`/`NR_PASSWORD` match the website login. Made-up credentials get exactly the same errors, so the message doesn't say which one is wrong. trackside stops retrying after the second refusal, so restart it once fixed. |
| `GroupAuthorizationFailed` or `SaslAuthenticationFailed` | The Rail Data Marketplace refused the Kafka login. Re-copy the key, secret and **group** from the product's Pub/Sub tab. The group must be the one issued. |
| `no full timetable loaded yet` | Run `import-schedule` once before daily updates can apply. |
| `update sequence ... already applied` | That update file is already loaded; this is harmless. A warning about a sequence **gap** means an update was missed. Re-run `import-schedule` to reload the full timetable. |
| Boards are empty | Check `import-schedule` finished. Services are only resolved from yesterday to 7 days ahead. |
| A train has no live times | TRUST only reports trains activated after you connected, and Darwin only sends what changes. Coverage fills in over the first few hours. |
| Station names in capitals ("CLAPHAM JUNCTION") | Names come from CORPUS until Darwin reference data is loaded. |

## Configuration

All settings come from environment variables. A `.env` file in the working
directory is read automatically, and real environment variables take
precedence over it.

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | (required) | Postgres connection string |
| `NR_USERNAME`, `NR_PASSWORD` | | Network Rail open data credentials (see [Setup](#1-get-a-network-rail-open-data-account)). Without them there is no live data and no downloads. |
| `NR_CLIENT_ID` | `trackside-<hostname>` | Names the connection and its durable subscriptions. Must be unique per instance. |
| `NR_STOMP_ADDR` | `publicdatafeeds.networkrail.co.uk:61618` | |
| `NR_FILES_URL` | `https://publicdatafeeds.networkrail.co.uk` | |
| `RDM_<FEED>_USERNAME`, `_PASSWORD`, `_GROUP` | | Rail Data Marketplace subscription for `<FEED>`: `DARWIN`, `TRUST`, `VSTP` or `TD`. Copy the values from the product's Pub/Sub tab. |
| `RDM_<FEED>_TOPIC` | see below | Override the topic |
| `RDM_BOOTSTRAP` | `pkc-z3p1v0.europe-west2.gcp.confluent.cloud:9092` | Kafka bootstrap server |
| `LISTEN_ADDR` | `:8080` | |
| `LOG_LEVEL` | `info` | `debug` logs every request |

Default topics are `TRAIN_MVT_ALL_TOC`, `VSTP_ALL`, `TD_ALL_SIG_AREA` and
`prod-1010-Darwin-Train-Information-Push-Port-IIII2_0-JSON`. The XML and
base64 Darwin topics work too.

Train Describer starts only once SMART data has been loaded with
`import-smart`. TD is the busiest feed, but trackside ignores signalling
messages and only queries the database for berth steps that SMART maps to a
station.

## API

All times are UK local time in RFC 3339 format, for example
`2026-10-06T08:07:00+01:00`. Locations can be given by CRS code (`CLJ`) or by
TIPLOC (`CLPHMJN`). A CRS code covers every TIPLOC at that station.

| Endpoint | |
|---|---|
| `GET /v1/locations?q=clapham` | Search locations |
| `GET /v1/locations/{code}` | One location and its TIPLOCs |
| `GET /v1/locations/{code}/departures` | Departure board |
| `GET /v1/locations/{code}/arrivals` | Arrivals board |
| `GET /v1/services/{uid}/{YYYY-MM-DD}` | A service's full route with live times and Train Describer position |
| `GET /healthz` | Liveness |

Board parameters:

- `at`: start of the window. Use RFC 3339 or `YYYY-MM-DDTHH:MM`. Defaults to now.
- `window`: minutes, from 1 to 1440. Default 120.
- `to`: departures only. Keep trains that call later at this location.
- `from`: arrivals only. Keep trains that called earlier at this location.
- `passes=true`: include trains that pass without stopping.

Each stop has `arrival`, `departure` or `pass` times. Each of those carries
`public` (timetable), `working` (internal schedule), `actual` (reported by
TRUST, Darwin or TD), `estimated` (Darwin's forecast, or projected from the
latest delay), `delayMinutes` and `delayed` (late by an unknown amount). Stops
also report `atPlatform`, and platforms carry `confirmed`. Services include
`lateReason` and `cancelReason` text when Darwin gives them. A cancelled
service also gets `cancelReasonCode`, the TRUST delay attribution code (for
example `IA`), and `cancelReasonCodeDescription`, the industry description of
that code ("Signal failure (including no fault found)").

### Realtime Trains–compatible endpoints

These copy the shape of the legacy `api.rtt.io/api/v1/json` API:

```
GET /api/v1/json/search/{station}[/to/{station}][/{yyyy}/{mm}/{dd}[/{hhmm}]][/arrivals]
GET /api/v1/json/service/{uid}/{yyyy}/{mm}/{dd}
```

Window used by each search form:

| Request | Window |
|---|---|
| No date | From 15 minutes ago to 2 hours ahead |
| Date only | The whole day |
| Date and time | 2 hours from that time |

The legacy API's own windows were never documented, so these were chosen to
be sensible.

There is no authentication. A client that sends Basic auth works unchanged.

Known differences from the original:

- Association data is not given.
- `serviceLocation` only reports `AT_PLAT`, not the approach states.
- Cancellation reason text comes from Darwin. Without Darwin it falls back to
  the industry description of the TRUST delay code, which is less
  passenger-friendly.

This project has no connection with Realtime Trains. It uses none of their
data or services and only copies the JSON layout so clients can migrate.

## How it works

```
 SCHEDULE (daily) ──┐
 VSTP (stream) ─────┼─► schedules + schedule_locations ─► services (per run date) ─► API
 CORPUS ────────────┘                                        ▲
 TRUST (stream) ──────────┐                                  │
 TD + SMART (stream) ─────┴──► service_events ───────────────┤
 Darwin (stream) ─────────────► darwin_forecasts ────────────┘
```

- **Schedules** are stored as published. Times are kept as seconds after
  midnight of the run date, so a train that crosses midnight just has values
  of 86400 or more.
- **Services** are worked out for yesterday through 7 days ahead. For each
  train UID and date, the lowest STP indicator wins (C < N < O < P), and a
  VSTP schedule beats a CIF one on a tie. A winning cancellation keeps the
  schedule it cancels, so the train can be shown as cancelled.
- **Bank holidays need no special handling.** `CIF_bank_holiday_running` only
  adds the "BHX" symbol to printed timetables. The real bank holiday changes
  are published as STP overlays and cancellations, which the rule above
  already applies. trackside deliberately ignores the flag, because acting on
  it would remove trains that do run.
- **TRUST** movements are matched to a stop by STANOX and planned time.
  Most TRUST timestamps are UK local time encoded as if it were UTC, and are
  decoded that way. Activations take their run date from
  `origin_dep_timestamp`, because `tp_origin_timestamp` is wrong for trains
  starting just after midnight in BST.
- **Precedence** for actual times: TRUST, then Darwin, then TD. For estimates,
  Darwin's forecast beats trackside's projection.
- **Live data is keyed by stop position.** It also records the TIPLOC, so if a
  schedule changes while the train runs, the data is re-attached to the right
  stop.
- **TD steps** are matched to a service by headcode, the SMART location and
  the nearest working time, preferring trains TRUST has activated.

## Development

```bash
go test ./...                                   # unit tests
TEST_DATABASE_URL=postgres://localhost/trackside_test go test ./...   # + end-to-end (wipes that DB)
```

The end-to-end test loads a small hand-written timetable from `testdata/`. It
covers overlays, a cancellation, a train past midnight, a daily update and a
VSTP train. It then replays TRUST messages and checks both APIs.

## Roadmap

- [x] Darwin Push Port: forecasts, platforms, cancellations, reasons and names
- [x] Rail Data Marketplace (Kafka) for all feeds
- [x] Train Describer: TD-derived times, berth position and "at platform"
- [ ] **Run against the live feeds** and fix whatever real data turns up
- [ ] Darwin associations (joins and divides) and station messages (`OW`)
- [ ] "Approaching" states from TD berth offsets
- [x] Cancellation reason text from the delay attribution codes when there's
  no Darwin
- [ ] OpenAPI spec, rate limiting and response caching for public instances.

## Data licences and attribution

If you run a public instance, you must credit the data sources:

- **Network Rail** data is under the
  [Network Rail Infrastructure Limited data feeds licence](https://www.networkrail.co.uk/who-we-are/transparency-and-ethics/transparency/open-data-feeds/network-rail-infrastructure-limited-data-feeds-licence/),
  which follows the Open Government Licence. Include: *"Contains public sector
  information licensed under the Open Government Licence v3.0."*
- **Darwin** is under the National Rail Enquiries terms. You
  must credit NRE as the source ("Powered by National Rail Enquiries"), keep
  forecasts consistent with Darwin, and show time-bound data such as platforms
  only when the feed allows.

The delay code descriptions in `internal/trust/delay_codes.json` come from
Section S of the
[Delay Attribution Principles and Rules](https://www.networkrail.co.uk/wp-content/uploads/2025/06/April-2025-DAPR.pdf)
(April 2025), published by the Delay Attribution Board.

Check the current terms yourself before you run a public service.

## Licence

trackside is free software under the [GNU Affero General Public License
v3.0](LICENSE). You can use, change and host it. If you run a modified
version as a network service, you must publish your changes under the same
licence.

The data it serves is not covered by this licence. Network Rail and National
Rail data have their own terms; see
[Data licences and attribution](#data-licences-and-attribution).
