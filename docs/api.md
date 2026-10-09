# API

The full reference is an OpenAPI 3.1 spec served at `/openapi.yaml`, and
rendered at `/docs` on any running instance. The source is
[`internal/api/openapi.yaml`](../internal/api/openapi.yaml).

## Authentication

An instance started with `API_KEY` needs the key on every request except
`/healthz`, as `Authorization: Bearer <key>` or as the password of HTTP Basic
auth with any user name. Without the key the answer is `401`. An instance
without `API_KEY` is open. See [API key](running.md#api-key).

```bash
curl -H "Authorization: Bearer $KEY" 'https://trains.example.com/v1/locations/CLJ/departures'
```

## Conventions

All times are UK local time in RFC 3339 format, for example
`2026-10-06T08:07:00+01:00`. Locations can be given by CRS code (`CLJ`) or by
TIPLOC (`CLPHMJN`). A CRS code covers every TIPLOC at that station.

## Endpoints

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
| `POST /v1/activities/register`, `DELETE /v1/activities/{id}` | iOS Live Activity push registration (see [iOS Live Activities](ios-live-activities.md)) |
| `GET /healthz` | Liveness |
| `GET /openapi.yaml`, `GET /docs` | API reference |

### Boards

Parameters:

- `at`: start of the window. Use RFC 3339 or `YYYY-MM-DDTHH:MM`. Defaults to now.
- `window`: minutes, from 1 to 1440. Default 120.
- `to`: departures only. Keep trains that call later at this location.
- `from`: arrivals only. Keep trains that called earlier at this location.
- `passes=true`: include trains that pass without stopping.

Boards include the station's `messages` from Darwin, most severe first, as
plain `text` and simple `html`. Messages Darwin marks as suppressed are left
out.

### Services

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

## Live updates

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

Details for client authors:

- **Unsubscribe** with `{"type":"unsubscribe","topics":[...]}`.
- **Up to 64 topics** per connection.
- **Errors** come back as `{"type":"error","message":"..."}` (bad JSON, an
  unknown message type or topic, too many topics) without closing the
  connection.
- **Slow clients** that fall far behind are disconnected. Reconnect,
  resubscribe and refetch.
- **Origin:** browser pages from other sites are refused. Clients that send no
  `Origin` header, such as mobile apps, are accepted.
- **API key:** on a keyed instance the WebSocket needs it too. Browsers send
  the Basic auth they already have for the page; other clients set the
  `Authorization` header on the upgrade request.
- **Proxies** must pass WebSocket upgrades and allow long idle connections
  (see [Putting it on the internet](running.md#putting-it-on-the-internet)).

## Realtime Trains–compatible endpoints

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

A client that sends Basic auth works unchanged. On an instance with
`API_KEY` set, use the key as the password.

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
