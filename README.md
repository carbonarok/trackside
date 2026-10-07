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
- **Associations:** trains that divide, join or form the next working, from
  the timetable, with Darwin's live changes on top.
- **Live running** from TRUST: activations, actual times, platform changes,
  cancellations, reinstatements, and changes of origin or identity. Delays are
  carried forward to estimate times at later stops.
- **Darwin forecasts** from National Rail: estimated times, live platforms
  (only the booked platform shows while National Rail suppresses the live one), cancellations, and readable delay and
  cancellation reasons. Darwin's estimates replace trackside's own wherever it
  has them.
- **Station messages** from Darwin: the disruption and information notices
  shown on station screens, attached to every board.
- **Train Describer positions:** signalling berth steps are mapped to arrivals
  and departures with the SMART data, giving times ahead of TRUST, the train's
  current berth, and "approaching" and "at platform" status.
- **Two ways to connect:** each Network Rail feed can come from Network Rail's
  own STOMP service or from the Rail Data Marketplace's Kafka. Darwin comes
  from the Rail Data Marketplace.
- **A website built in:** station boards, train pages with the route drawn
  as a line diagram, a search that takes stations, headcodes or train IDs, a
  live map of every train, and a Delay Repay checker. It is compiled into the
  binary, so there's nothing extra to host. Open `http://localhost:8080/`.
- **Live train map:** every running train, coloured by how late it is and
  pointing the way it's going. Positions come from the last reported location
  and the timetable, so trains between stations are estimates. Also available
  as GeoJSON.
- **Live updates:** boards, train pages and the map refresh when a train
  reports, not on a timer. A WebSocket (`/v1/live`) tells each page what
  changed, and API clients can use it too.
- **History:** each day's actual times are kept (400 days by default), with
  punctuality stats per station and per train.
- **Delay Repay checker:** how late a journey arrived, which compensation
  band that falls into, and the next train if yours was cancelled.
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
| Darwin Lite (OpenLDBWS) | The same forecasts, on demand, one station at a time | National Rail | Optional, if you lack the Push Port |
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

**Timetable and reference files from the Rail Data Marketplace.** The
marketplace offers SCHEDULE ("NWR Schedule"), CORPUS ("NWR CORPUS"), SMART
("NWR SMART") and Darwin reference data ("Darwin Timetable Files") as file
products. You can download them by hand from each product's **Data files**
tab and import them with `import-schedule FILE` and so on. To keep the
timetable current automatically, have the marketplace deliver them to an
[inbox](#automatic-file-delivery-inbox) instead.

**Network Rail feeds through the Rail Data Marketplace.** The marketplace
also carries Network Rail's feeds over Kafka. This is useful if Network
Rail's platform is full. Subscribe to the Train Movements, VSTP or TD
product and set `RDM_TRUST_*`, `RDM_VSTP_*` or `RDM_TD_*` in the same way.
Any feed configured like this is read from Kafka instead of Network Rail.

The marketplace delivers SCHEDULE, CORPUS and SMART as files to your own
storage too. Load them with `import-schedule FILE`, `import-corpus FILE` and
`import-smart FILE`.

### Alternative: Darwin Lite

If you don't have the Darwin Push Port, or are still waiting for approval, a
**Darwin Lite** token gives you the same Darwin forecasts on demand. Darwin
Lite is National Rail's OpenLDBWS (Live Departure Boards Web Service). There's
no login: the developer token from the registration email is the credential.

```bash
NRE_LDBWS_TOKEN=<your token>
```

When someone asks for a board, trackside fetches that station's live board
from Darwin Lite and merges in expected times, platforms, cancellations,
reasons and station messages:

- **Caching and rate limit:** each station is cached for a minute. trackside
  stays under 4,500 requests an hour, below the free tier's cap of 5,000.
- **Matching:** Darwin Lite has no train UIDs, so trains are matched on booked
  time, origin, destination and operator.
- **Service detail:** once a train has appeared on a fetched board, its
  service detail is live along the whole route.
- **Coverage:** only from 2 hours ago to 4 hours ahead, since that's what the
  service answers.
- **Precedence:** if the Darwin Push Port is configured too, it takes over and
  Darwin Lite isn't used.

### Automatic file delivery (inbox)

trackside can watch a folder or cloud bucket and import whatever the Rail
Data Marketplace delivers there:

- **Which files:** CORPUS, SMART, Darwin reference data and SCHEDULE.
- **Timetable order:** updates are applied in sequence. If there's no
  timetable yet, or an update is missing, the newest full file is used.
- **Bookkeeping:** each file is imported once. CIF-format copies and
  unrelated files are ignored.
- **When:** every 15 minutes (`INBOX_INTERVAL`), or on demand with
  `trackside import-inbox`.

The marketplace can deliver to Amazon S3, Google Cloud Storage, Azure Blob
Storage or an SFTP server you run. **Google Cloud Storage is the free
option.** Its Always Free tier gives 5 GB of storage and 100 GB of downloads a
month with no time limit, though it needs a billing account (a card) on the
Google Cloud project.

1. **Create the bucket.** In the [Google Cloud console](https://console.cloud.google.com/storage),
   create a bucket: Region `us-central1`, `us-east1` or `us-west1` (the free
   tier regions), Standard storage class.
2. **Stop it filling up.** Under the bucket's **Lifecycle** tab, add a rule
   to delete objects older than 7 days.
3. **Add it as a destination in the marketplace.** Go to **Manage → My file
   transfers → Add file destination**, choose Google Cloud Storage and enter
   the bucket name. The form shows the marketplace's service accounts. On the
   bucket's **Permissions** tab, grant them Storage Object Viewer, Storage
   Legacy Bucket Reader, Storage Bucket Viewer and Storage Legacy Bucket
   Writer. Then **Validate** and **Submit**.
4. **Point each product at it.** On each product's **Data files → File
   transfers** tab, choose the destination: NWR Schedule, NWR CORPUS, NWR
   SMART and Darwin Timetable Files.
5. **Give trackside read access.** Under **Cloud Storage → Settings →
   Interoperability**, create an HMAC key for a service account that has
   Storage Object Viewer on the bucket. Then:

   ```bash
   INBOX_BUCKET=gs://your-bucket
   INBOX_ACCESS_KEY=GOOG1E...
   INBOX_SECRET_KEY=...
   ```

For SFTP delivery, or files you download by hand, point `INBOX_DIR` at the
folder instead. A dedicated folder is best, but a busy one such as Downloads
also works, because unrecognised files are ignored.

### 3. Run it

With Docker:

```bash
cp .env.example .env        # then fill in the credentials from steps 1 and 2
docker compose up -d        # starts Postgres and the server on :8080
docker compose run --rm trackside import-corpus
docker compose run --rm trackside import-schedule
docker compose run --rm trackside import-smart     # optional, for Train Describer
```

Without Docker (Go 1.25+, Node 22+ and Postgres 14+):

```bash
make build                  # builds the website, then the binary that embeds it
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

**Check it works:** open <http://localhost:8080/> for the website, or

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
| `INBOX_DIR` | | Folder to import delivered files from |
| `INBOX_BUCKET` | | `gs://bucket/prefix` or `s3://bucket/prefix` to import delivered files from |
| `INBOX_ACCESS_KEY`, `INBOX_SECRET_KEY` | | HMAC (Google Cloud Storage) or access keys (S3) for `INBOX_BUCKET` |
| `INBOX_ENDPOINT` | provider default | Override the bucket's S3 endpoint |
| `INBOX_INTERVAL` | `15m` | How often to check the inbox |
| `NRE_LDBWS_TOKEN` | | Darwin Lite developer token (see [Darwin Lite](#alternative-darwin-lite)). Ignored when the Darwin Push Port is configured. |
| `NRE_LDBWS_URL` | `https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx` | Darwin Lite endpoint |
| `HISTORY_DAYS` | `400` | How many days of actual running to keep. `0` keeps it forever. |
| `MAP_TILE_URL` | OpenStreetMap | Map tiles for the live map, as `https://.../{z}/{x}/{y}.png`. OpenStreetMap's own servers are fine for personal use; a public instance should use its own tiles or a tile provider. |
| `APNS_KEY_FILE` or `APNS_KEY` | | The APNs auth key (.p8) from the Apple Developer portal, as a file path or its contents (`\n` may stand for line breaks). Turns on [Live Activity pushes](#ios-live-activities). |
| `APNS_KEY_ID`, `APNS_TEAM_ID` | | The key's ID and your Apple team ID |
| `APNS_ENV` | `production` | APNs environment tried first, `production` or `sandbox`. A token from the other one is retried there and remembered, so Xcode and TestFlight builds both work. |
| `APNS_BUNDLE_IDS` | (any) | Comma-separated bundle IDs allowed to register |
| `MAP_TILE_ATTRIBUTION` | | Attribution shown with `MAP_TILE_URL`'s tiles (HTML) |
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

The full reference is an OpenAPI 3.1 spec served at `/openapi.yaml`, and
rendered at `/docs` on any running instance. The source is
[`internal/api/openapi.yaml`](internal/api/openapi.yaml).

All times are UK local time in RFC 3339 format, for example
`2026-10-06T08:07:00+01:00`. Locations can be given by CRS code (`CLJ`) or by
TIPLOC (`CLPHMJN`). A CRS code covers every TIPLOC at that station.

| Endpoint | |
|---|---|
| `GET /v1/locations?q=clapham` | Search locations |
| `GET /v1/locations/{code}` | One location and its TIPLOCs |
| `GET /v1/locations/{code}/departures` | Departure board |
| `GET /v1/locations/{code}/arrivals` | Arrivals board |
| `GET /v1/locations/{code}/messages` | Darwin station messages |
| `GET /v1/services?q=1A23` | Find trains by headcode or train ID (`date` defaults to today) |
| `GET /v1/services/{uid}/{YYYY-MM-DD}` | A service's full route with live times and Train Describer position |
| `GET /v1/history/services/{uid}` | How a train has run on past days |
| `GET /v1/stats/locations/{code}` | Punctuality at a station |
| `GET /v1/delay-repay?from=BTN&to=VIC&date=…&departure=08:15` | How late a journey arrived, and its Delay Repay band |
| `GET /v1/map/trains`, `GET /v1/map/stations` | Train positions and stations as GeoJSON |
| `GET /v1/live` | WebSocket: hear when boards, trains and the map change (see below) |
| `POST /v1/activities/register`, `DELETE /v1/activities/{id}` | iOS Live Activity push registration (see below) |
| `GET /healthz` | Liveness |
| `GET /openapi.yaml`, `GET /docs` | API reference |

Board parameters:

- `at`: start of the window. Use RFC 3339 or `YYYY-MM-DDTHH:MM`. Defaults to now.
- `window`: minutes, from 1 to 1440. Default 120.
- `to`: departures only. Keep trains that call later at this location.
- `from`: arrivals only. Keep trains that called earlier at this location.
- `passes=true`: include trains that pass without stopping.

Boards include the station's `messages` from Darwin, most severe first, as
plain `text` and simple `html`. Messages Darwin marks as suppressed are left
out.

Service detail includes `associations`. Each one gives the type from this
train's point of view (`divides`, `divided_from`, `joined_by`, `joins`,
`forms`, `formed_from` or `linked`), the location, whether it's cancelled, and
the other train.

Each stop has `arrival`, `departure` or `pass` times. Each of those carries
`public` (timetable), `working` (internal schedule), `actual` (reported by
TRUST, Darwin or TD), `estimated` (Darwin's forecast, or projected from the
latest delay), `delayMinutes` and `delayed` (late by an unknown amount). Stops
also report `approaching` and `atPlatform`, and platforms carry `confirmed`. Services include
`lateReason` and `cancelReason` text when Darwin gives them. A cancelled
service also gets `cancelReasonCode`, the TRUST delay attribution code (for
example `IA`), and `cancelReasonCodeDescription`, the industry description of
that code ("Signal failure (including no fault found)").

### Live updates

`/v1/live` is a WebSocket that tells you when something changed, so you
refetch only then instead of polling. The website uses it for boards, train
pages and the map.

```js
const ws = new WebSocket('ws://localhost:8080/v1/live')
ws.onopen = () => ws.send(JSON.stringify({ type: 'subscribe', topics: ['station:CLJ', 'train:W12345|2026-10-06'] }))
ws.onmessage = e => console.log(JSON.parse(e.data)) // {"type":"changed","topics":["station:CLJ"]}
```

Topics are `station:<CRS or TIPLOC>`, `train:<uid>|<YYYY-MM-DD>` and `map`.
Changes are gathered for 2 seconds and sent together. A train's change is sent
to every station it calls at or passes, since a delay carries forward along
the route. Messages say what changed, not how: refetch with the REST
endpoints. The first message is `{"type":"hello","fallback":120}`, the number
of seconds to keep polling at anyway. It is 30 with Darwin Lite, whose boards
are fetched on demand and never pushed.

### iOS Live Activities

With an APNs key configured, trackside keeps the TrackSide iOS app's Live
Activities current while the app is in the background. The app registers
each activity's push token for one leg of a journey:

```http
POST /v1/activities/register
{"push_token": "<hex>", "activity_id": "<id>", "service_uid": "W12345", "run_date": "2026-10-07",
 "origin_crs": "WOK", "destination_crs": "RDG", "bundle_id": "com.example.TrackSideIOS",
 "connection_minutes": 7, "phase": "boarding"}
```

`connection_minutes` and `phase` are optional: the minutes to make this train
from the previous leg, and the phase the app is showing. Better than a fixed
`connection_minutes` is the previous leg's train: with `previous_service_uid`
(and `previous_run_date` and `previous_arrival_crs` when they differ from this
leg's date and origin), the connection is worked out from both trains' live
times and pushed again whenever either train changes. Registering again
with the same `activity_id` replaces the token. `DELETE
/v1/activities/{activity_id}` stops pushes.

Whenever the train changes, the leg's `content-state` is rebuilt from the
same data as `/v1/services/{uid}/{date}` and pushed with `apns-push-type:
liveactivity`, but only if something the activity shows has changed. Dates
in it (`departureDate`, `arrivalDate`, `lastUpdated`) are seconds since 1
January 2001, which is how Swift decodes a `Date` by default. An alert,
sent at priority 10, accompanies a platform change, a cancellation, or a
delay that grows by 3 minutes or more to at least 5, with the default sound.
Other updates go at priority 5. iOS shows a Live Activity's alert on the
activity rather than as a notification, so if the registration includes the
device's own APNs token (`device_token`) the alert is sent to it as a normal
notification instead, and the activity updates silently. A platform that
changes while the leg is tracked stays marked changed until the leg ends.

Arrival at the destination sends an `end` event and removes the
registration, as does an APNs `410`. Registrations with nothing sent for six
hours are removed too.

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

- Associations appear on the location where they happen, as
  `{type, associatedUid, associatedRunDate}` with types `divide`, `join`,
  `next`, `prev` and `linked`. The legacy layout wasn't documented, so this is
  a best guess.
- `serviceLocation` reports `APPR_STAT` and `AT_PLAT`. It doesn't report
  `APPR_PLAT`, `DEP_PREP` or `DEP_READY`, which need signalling data trackside
  doesn't decode.
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
  Darwin's forecast beats trackside's projection. Darwin Lite data is treated
  as Darwin, but only fills stops the Darwin stream hasn't covered.
- **Live data is keyed by stop position.** It also records the TIPLOC, so if a
  schedule changes while the train runs, the data is re-attached to the right
  stop.
- **Restarts:** trains that TRUST activated before trackside started are
  picked up from their first movement. The headcode and start day in the
  TRUST train ID, plus the location and planned time, identify the service.
- **TD steps** are matched to a service by headcode, the SMART location and
  the nearest working time, preferring trains TRUST has activated.
- **Approaching** means the train has stepped into the berth that a SMART
  arrival step starts from. It clears when the arrival is reported, or after
  20 minutes without one.

## Development

```bash
go test ./...                                   # unit tests
TEST_DATABASE_URL=postgres://localhost/trackside_test go test ./...   # + end-to-end (wipes that DB)
```

The website is a React app in [`web/`](web/). `make dev` runs it with hot
reload on <http://localhost:5173>, talking to a trackside on :8080. `make
build` builds it into `web/dist` and then builds the binary, which embeds it.
A binary built without `make web` first still works, but `/` says the
website is missing.

A test checks that `openapi.yaml` and the response structs list exactly the
same fields. If you change a response, update the spec too.

The end-to-end test loads a small hand-written timetable from `testdata/`. It
covers overlays, a cancellation, a train past midnight, a daily update and a
VSTP train. It then replays TRUST messages and checks both APIs.

## Roadmap

- [x] Darwin Push Port: forecasts, platforms, cancellations, reasons and names
- [x] Rail Data Marketplace (Kafka) for all feeds
- [x] Train Describer: TD-derived times, berth position and "at platform"
- [x] **Run against the live feeds** and fix whatever real data turns up
- [x] Website: boards, train pages, search, live map, Delay Repay checker
- [ ] GTFS and GTFS-Realtime export
- [x] Associations (joins, divides, next workings) from the timetable and Darwin
- [x] Darwin station messages (`OW`)
- [x] "Approaching" state from Train Describer
- [x] Cancellation reason text from the delay attribution codes when there's
  no Darwin
- [x] OpenAPI spec and docs page
- [ ] Rate limiting and response caching for public instances

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
