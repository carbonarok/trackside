# trackside

An open-source server for UK train times. It loads the GB rail timetable,
follows trains live, and serves departure boards and service details as JSON
and as a website.

It is built only from public open data feeds: Network Rail's timetable, train
movements and Train Describer, and National Rail's Darwin. Anyone can run their
own copy for free, with their own feed credentials. There's also a companion
[iOS app](https://github.com/carbonarok/TrackSideIOS) with widgets and Live
Activities.

> [!WARNING]
> **Built on vibes.** trackside was written quickly, with a lot of help from
> an AI coding assistant, by someone annoyed that their train API started
> charging. It has tests, the data formats were checked against the
> published documentation, and it now runs against the live feeds, but only
> on a few instances and not for long. Expect bugs, odd edge cases and times
> that are occasionally wrong. **Don't rely on it for anything important**,
> such as making a connection you can't afford to miss, and check against
> official sources (National Rail Enquiries, station screens, operator apps).
> Bug reports and pull requests are very welcome.

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
- **Optional API key** for a private instance. The website, the iOS app and
  Realtime Trains clients all work with it.
- **Easy to run.** It is a single static binary plus Postgres, using about
  125 MB of memory.

## Quick start

Getting feed access takes the longest, because both data providers approve
accounts by hand, so **request access first**: see
[Getting the data](docs/data-feeds.md). Then:

```bash
git clone https://github.com/carbonarok/trackside && cd trackside
cp .env.example .env        # fill in the credentials you have
docker compose up -d        # Postgres and the server on :8080
docker compose run --rm trackside import-corpus
docker compose run --rm trackside import-schedule   # the timetable; boards appear when it finishes
```

Open <http://localhost:8080/>, or:

```bash
curl 'localhost:8080/v1/locations/CLJ/departures'
```

Building without Docker, the first imports, deployment behind HTTPS and
troubleshooting are covered in [Running and deploying](docs/running.md).

> [!IMPORTANT]
> Run **one copy per set of feed credentials.** A second trackside with the
> same Rail Data Marketplace settings (say, a test run on your laptop in a
> folder holding the server's `.env`) silently takes part of every live feed
> away from the first. See
> [why](docs/running.md#one-instance-per-set-of-credentials).

## Documentation

| | |
|---|---|
| [Getting the data](docs/data-feeds.md) | Which feeds you need, Network Rail and Rail Data Marketplace accounts, Darwin Lite, automatic file delivery |
| [Running and deploying](docs/running.md) | Docker and bare metal, first imports, the API key, HTTPS and WebSockets behind a proxy, the container image, sizing, troubleshooting |
| [API](docs/api.md) | Endpoints, response fields, the live-update WebSocket, the Realtime Trains–compatible API. The full OpenAPI reference is served at `/docs` on every instance. |
| [iOS Live Activities](docs/ios-live-activities.md) | Creating an APNs key and pushing Live Activity updates to the TrackSide app |
| [How it works](docs/how-it-works.md) | How the feeds are combined into one timetable with live times |
| [Contributing](CONTRIBUTING.md) | Development setup, tests and conventions |

## Configuration

All settings come from environment variables. A `.env` file in the working
directory is read automatically, and real environment variables take
precedence over it. [`.env.example`](.env.example) lists them with comments.

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | (required) | Postgres connection string |
| `API_KEY` | | Require this key on every request except `/healthz`. Comma-separate several to rotate. Empty means open. See [API key](docs/running.md#api-key). |
| `NR_USERNAME`, `NR_PASSWORD` | | Network Rail open data credentials (see [Getting the data](docs/data-feeds.md#network-rail-open-data-account)). Without them there is no live data and no downloads. |
| `NR_CLIENT_ID` | `trackside-<hostname>` | Names the connection and its durable subscriptions. Must be unique per instance. |
| `NR_STOMP_ADDR` | `publicdatafeeds.networkrail.co.uk:61618` | |
| `NR_FILES_URL` | `https://publicdatafeeds.networkrail.co.uk` | |
| `RDM_<FEED>_USERNAME`, `_PASSWORD`, `_GROUP` | | Rail Data Marketplace subscription for `<FEED>`: `DARWIN`, `TRUST`, `VSTP` or `TD`. Copy the values from the product's Pub/Sub tab. |
| `RDM_<FEED>_TOPIC` | see below | Override the topic |
| `RDM_BOOTSTRAP` | `pkc-z3p1v0.europe-west2.gcp.confluent.cloud:9092` | Kafka bootstrap server |
| `INBOX_DIR` | | Folder to import delivered files from (see [inbox](docs/data-feeds.md#automatic-file-delivery-inbox)) |
| `INBOX_BUCKET` | | `gs://bucket/prefix` or `s3://bucket/prefix` to import delivered files from |
| `INBOX_ACCESS_KEY`, `INBOX_SECRET_KEY` | | HMAC (Google Cloud Storage) or access keys (S3) for `INBOX_BUCKET` |
| `INBOX_ENDPOINT` | provider default | Override the bucket's S3 endpoint |
| `INBOX_SFTP` | | `sftp://user@host[:port]/path` to import delivered files from |
| `INBOX_SFTP_PASSWORD`, `INBOX_SFTP_KEY` | | Password and/or private key (OpenSSH or PEM) for `INBOX_SFTP` |
| `INBOX_SFTP_HOST_KEY` | | The SFTP server's public key, as `ssh-keyscan` prints it. Required. |
| `INBOX_INTERVAL` | `15m` | How often to check the inbox |
| `NRE_LDBWS_TOKEN` | | Darwin Lite developer token (see [Darwin Lite](docs/data-feeds.md#darwin-lite)). Ignored when the Darwin Push Port is configured. |
| `NRE_LDBWS_URL` | `https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx` | Darwin Lite endpoint |
| `HISTORY_DAYS` | `400` | How many days of actual running to keep. `0` keeps it forever. |
| `MAP_TILE_URL` | OpenStreetMap | Map tiles for the live map, as `https://.../{z}/{x}/{y}.png`. OpenStreetMap's own servers are fine for personal use; a public instance should use its own tiles or a tile provider. |
| `APNS_KEY_FILE` or `APNS_KEY` | | The APNs auth key (.p8) from the Apple Developer portal, as a file path or its contents (`\n` may stand for line breaks). Turns on [Live Activity pushes](docs/ios-live-activities.md). |
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
- [x] Shared response caching for public instances
- [x] Optional API key
- [ ] Rate limiting for public instances
- [ ] Per-user API keys

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
