# Running and deploying

- [Run it](#run-it)
- [First imports](#first-imports)
- [What happens on its own](#what-happens-on-its-own)
- [One instance per set of credentials](#one-instance-per-set-of-credentials)
- [API key](#api-key)
- [Putting it on the internet](#putting-it-on-the-internet)
- [Container image](#container-image)
- [Sizing](#sizing)
- [Troubleshooting](#troubleshooting)

## Run it

With Docker:

```bash
cp .env.example .env        # then fill in the credentials (see data-feeds.md)
docker compose up -d        # starts Postgres and the server on :8080
docker compose run --rm trackside import-corpus
docker compose run --rm trackside import-schedule
docker compose run --rm trackside import-smart     # optional, for Train Describer
```

Every setting in `.env` is passed into the container. The compose file sets
`DATABASE_URL` and `LISTEN_ADDR` itself, so leave those for the container.

The `import-*` commands without a file download from Network Rail and so need
`NR_USERNAME` and `NR_PASSWORD`. With only a Rail Data Marketplace account,
put the downloaded files in a folder, mount it and import from there, or let
the [inbox](data-feeds.md#automatic-file-delivery-inbox) do it:

```bash
docker compose run --rm -v ~/Downloads:/files trackside import-schedule /files/CIF_ALL_FULL_DAILY_toc-full.json.gz
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

Database migrations run automatically before every command, so there is no
separate migration step, including after an upgrade.

## First imports

- `import-corpus` and `import-smart` take a few seconds.
- `import-schedule` loads the full timetable: about 140 MB compressed, 1 GB
  of JSON, 730,000 schedules and 10 million stops. On a fast machine with an
  SSD it takes a minute or two. On a small VM or network storage, allow 15
  minutes or more.
- **The timetable appears all at once.** The import is one transaction, so
  boards stay empty until it finishes, even though station search (from
  CORPUS) already works.
- Each import command also accepts a local file (`.json` or `.json.gz`)
  instead of downloading.

Check it worked: `curl 'localhost:8080/v1/locations/CLJ/departures'` should
list trains, and <http://localhost:8080/> shows the website.

## What happens on its own

Once `serve` is running:

- **Timetable updates:** the daily update is applied each morning, from 06:00,
  when Network Rail publishes it. This needs Network Rail credentials;
  otherwise the [inbox](data-feeds.md#automatic-file-delivery-inbox) does it.
- **VSTP trains** are added as they arrive.
- **The service window rolls forward:** services are re-resolved hourly for
  yesterday to 7 days ahead.
- **TRUST, Darwin and TD** are followed live.
- **Station coordinates** come from NaPTAN on the first start and weekly after
  that. They're retried every 10 minutes until stations exist to attach them
  to.
- **History** older than `HISTORY_DAYS` is deleted hourly.

The logs show `subscribed topic=...` for each Network Rail feed and
`consuming topic=...` for each Kafka feed.

**Live data fills in over the first few hours.** TRUST only reports trains
activated after trackside connected, and Darwin only sends what changes, so
right after a start many trains show only their timetable.

## One instance per set of credentials

Run one trackside per set of feed credentials. With the Rail Data
Marketplace, a second copy using the same `RDM_*` settings joins the same
Kafka consumer group, and the two then split every feed between them, each
missing part of it. On Network Rail, two copies with the same
`NR_CLIENT_ID` fight over the same durable subscriptions.

This catches people out in two ways:

- **A quick local test.** trackside reads `.env` from the directory it starts
  in. Running `trackside serve` in a folder that holds your server's `.env`
  joins the server's consumer groups. Keep separate credentials (or no feed
  credentials at all) for development.
- **Scaling out.** Don't run more than one replica. On Kubernetes, use one
  replica with the `Recreate` deployment strategy so old and new pods never
  overlap.

## API key

By default anyone who can reach the server can use it. To require a key, set:

```bash
API_KEY=$(openssl rand -hex 24)
```

Every request except `/healthz` then needs the key, either as
`Authorization: Bearer <key>` or as the password of HTTP Basic auth with any
user name:

- **The website** works unchanged. The browser asks for a user name and
  password once: enter anything as the user name and the key as the password.
- **The TrackSide iOS app** has a field for it in Settings → Server.
- **Realtime Trains–compatible clients** already send Basic auth. Put the key
  in as the password.
- **Health checks** (`/healthz`) stay open for load balancers and Kubernetes.

Comma-separate several keys (`API_KEY=old,new`) to change the key without
locking every client out at once. This is one shared key for a small group,
not per-user accounts. Use HTTPS in front of it, or the key travels in
plain text.

## Putting it on the internet

trackside is one HTTP server on `LISTEN_ADDR`. Put it behind something that
terminates TLS:

- **A reverse proxy** such as Caddy, nginx or Traefik.
- **A tunnel** such as Cloudflare Tunnel, if you'd rather not open any ports
  at home.

Whatever you use:

- **Let WebSockets through.** `/v1/live` is a long-lived WebSocket. The
  proxy must pass `Upgrade` and `Connection` headers and allow idle
  connections for at least a few minutes. nginx needs
  `proxy_http_version 1.1`, the two headers set, and a longer
  `proxy_read_timeout`. Caddy and Cloudflare handle this by default.
- **Keep the host name.** The WebSocket refuses browser pages from other
  sites, by comparing the `Origin` header with the `Host` the request
  arrived for.
- **Responses are already gzipped,** so there's no need to compress them
  again.
- **Map tiles:** the live map uses OpenStreetMap's tile servers by default,
  which is fine for personal use. A public instance should set
  `MAP_TILE_URL` to its own tiles or a tile provider.
- **Credit the data** as described in
  [Data licences and attribution](../README.md#data-licences-and-attribution).

A minimal Caddyfile:

```
trains.example.com {
	reverse_proxy localhost:8080
}
```

## Container image

CI publishes an image to the GitHub Container Registry for every push to
`main` and every `v*` tag:

```
ghcr.io/carbonarok/trackside:main          # latest main
ghcr.io/carbonarok/trackside:sha-<commit>  # a specific build
ghcr.io/carbonarok/trackside:v1.2.3        # a release
```

It is built for `linux/amd64` only. On ARM (a Raspberry Pi, Apple Silicon),
build your own with `docker build .`. The image runs as `nobody`, so pass the
APNs key as `APNS_KEY` rather than mounting a file only root can read.

## Sizing

Measured on one instance with every feed configured:

| | |
|---|---|
| Database after the first import | about 1.5 GB |
| History growth | about 30–40 MB a day, so about 12–16 GB at the default `HISTORY_DAYS=400` |
| Memory | about 125 MB steady, about 220 MB peak during an import |
| CPU | about 5% of a core for trackside and 17% for Postgres following all live feeds |

Give Postgres at least 20–30 GB of disk if you keep the default history, or
lower `HISTORY_DAYS`.

Boards, train pages and the map are cached and shared between viewers until
a train on them changes, so a busy board costs one database query however
many people are watching it. On a home connection, upload speed is usually
the limit before CPU is.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `401 Unauthorized` from an import, or `AMQ339009 Exception getting session` in the logs | Network Rail is refusing the login. Check, in order: the account has been activated (you get an email); you're subscribed to the feed under **My Feeds**; and `NR_USERNAME`/`NR_PASSWORD` match the website login. Made-up credentials get exactly the same errors, so the message doesn't say which one is wrong. trackside stops retrying after the second refusal, so restart it once fixed. |
| `GroupAuthorizationFailed` or `SaslAuthenticationFailed` | The Rail Data Marketplace refused the Kafka login. Re-copy the key, secret and **group** from that product's Pub/Sub tab. Each product has its own three, and the group must be the one issued. |
| `{"error":"unknown location ..."}` | CORPUS isn't loaded yet. Run `import-corpus`. |
| Stations are found but boards are empty | The timetable isn't loaded, or its import hasn't finished: it appears all at once when it commits. Check `import-schedule` completed. Services only exist from yesterday to 7 days ahead. |
| Live data has gaps, or stopped when you tested something | Another copy is using the same feed credentials. See [One instance per set of credentials](#one-instance-per-set-of-credentials). |
| `no full timetable loaded yet` | Run `import-schedule` once before daily updates can apply. |
| `update sequence ... already applied` | That update file is already loaded; this is harmless. A warning about a sequence **gap** means an update was missed. Re-run `import-schedule` to reload the full timetable. |
| A train has no live times | TRUST only reports trains activated after you connected, and Darwin only sends what changes. Coverage fills in over the first few hours. |
| Station names in capitals ("CLAPHAM JUNCTION"), or a big station missing trains | Darwin reference data isn't loaded. See [Darwin reference data](data-feeds.md#rail-data-marketplace-account). |
| The live map has no stations | NaPTAN loads on start and retries every 10 minutes; check the logs for `station coordinates failed`. |
| Every request gets `401` with `this server needs an API key` | `API_KEY` is set. Send it as described in [API key](#api-key). |
| Website loads but never updates live | The WebSocket isn't getting through your proxy. See [Putting it on the internet](#putting-it-on-the-internet). |
| `address already in use` | Another program has port 8080. Set `LISTEN_ADDR=:8090` (outside Docker) or change the published port in `docker-compose.yml`. |
