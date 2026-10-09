# iOS Live Activities

With an APNs key configured, trackside keeps the
[TrackSide iOS app](https://github.com/carbonarok/TrackSideIOS)'s Live
Activities current while the app is in the background: platform changes,
delays and connection times reach the lock screen and Dynamic Island without
the app running. Without a key, everything else works and the app updates its
Live Activities only while it's open.

- [What you need](#what-you-need)
- [Create the APNs key](#create-the-apns-key)
- [Configure trackside](#configure-trackside)
- [Check it works](#check-it-works)
- [Registration API](#registration-api)
- [What gets pushed](#what-gets-pushed)
- [Troubleshooting](#troubleshooting)

## What you need

- **A paid Apple Developer Program membership.** A free Personal Team can't
  use push notifications. Enrolment can take up to 48 hours after paying.
- **A real iPhone.** The Simulator can't get push tokens.
- **Your own build of the app,** with your team and bundle ID. A key only
  pushes to apps from the team that made it, so an instance can only serve
  builds signed by its own team. See the app's README.

## Create the APNs key

1. In the [Apple Developer portal](https://developer.apple.com/account/resources/authkeys/list),
   go to **Certificates, Identifiers & Profiles → Keys** and add a key.
2. Give it a name, tick **Apple Push Notifications service (APNs)**, and
   choose **Sandbox & Production** if asked.
3. **Download the key.** It's a file named `AuthKey_<KEY ID>.p8`. You can
   only download it once, so keep it somewhere safe.
4. Note the **Key ID** (the ten characters in the file name, also shown on
   the key's page) and your **Team ID** (under **Membership details**).

One key covers both the sandbox and production environments and every app
in your team. No certificates are involved.

## Configure trackside

```bash
APNS_KEY_FILE=/path/to/AuthKey_ABCDE12345.p8
APNS_KEY_ID=ABCDE12345
APNS_TEAM_ID=FGHIJ67890
APNS_BUNDLE_IDS=com.example.TrackSide
```

- **`APNS_KEY` instead of a file.** In a container or a secret store it's
  often easier to pass the key's contents. `.env` holds one line per
  setting, so write each line break as `\n`:

  ```bash
  APNS_KEY=-----BEGIN PRIVATE KEY-----\nMIGTAgEA...\n...\n-----END PRIVATE KEY-----
  ```

  `awk 'NF {printf "%s\\n", $0}' AuthKey_ABCDE12345.p8` prints it in that
  form. The container image runs as `nobody`, which is another reason to
  prefer `APNS_KEY` over a mounted file.
- **`APNS_BUNDLE_IDS`** is the app's bundle ID. Registrations from other
  bundle IDs are refused with `403`. Comma-separate several; leave it empty
  to accept any.
- **`APNS_ENV`** is the environment tried first: `production` (the default)
  or `sandbox`. It hardly matters, see below.

**Sandbox and production.** A build run from Xcode gets sandbox push tokens;
TestFlight and App Store builds get production ones. trackside tries
`APNS_ENV` first, and when Apple answers `BadDeviceToken` it retries the
other environment and remembers which one worked for that token. Both kinds
of build work against one server.

## Check it works

- **At start-up** the log says `live activity pushes on apns=...`.
- **Without a key** `POST /v1/activities/register` answers `503`, and the
  app quietly falls back to updating only while it's open.
- **When the app starts a Live Activity,** `LOG_LEVEL=debug` shows the
  `POST /v1/activities/register` request. Pushes are silent when they work;
  Apple refusing one logs `live activities: apns refused` with the reason.
- **To test the key without the app,** register with a made-up push token
  for a real train. Apple answering `BadDeviceToken` in the log means it
  accepted your key and only the token was wrong.

## Registration API

The app registers each activity's push token for one leg of a journey:

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

Answers:

| Status | Meaning |
|---|---|
| `201` | Registered |
| `200` | Registration replaced (same `activity_id`) |
| `400` | Not a valid registration. Unknown fields are refused too. |
| `403` | The bundle ID isn't in `APNS_BUNDLE_IDS` |
| `404` | No such service on that date |
| `422` | The train doesn't call at `origin_crs` and then `destination_crs`, or the previous train doesn't call at the connecting station |
| `503` | No APNs key on this server |

`origin_crs`, `destination_crs` and `previous_arrival_crs` must be three-letter
CRS codes, not TIPLOCs. `destination_crs` is where the passenger gets off,
which isn't necessarily where the train terminates.

## What gets pushed

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

The `content-state` must match the app's `LegActivityAttributes.ContentState`
field for field. iOS drops an update it can't decode without any error, so a
renamed field shows up only as a Live Activity that stops changing. Change
both together.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| The app logs `com.apple.ActivityKit.ActivityInput error 0` | The installed build has no `aps-environment` entitlement, so ActivityKit can't issue a push token. Add the Push Notifications capability (it needs a paid team), then clean the build folder and reinstall. |
| The server sees `DELETE /v1/activities/...` but never a registration | The same: the app can't get a push token, so it never registers. |
| `403` on registration | Add the app's bundle ID to `APNS_BUNDLE_IDS`. |
| `422` on registration | The stations aren't calling points of that train in that order, or a TIPLOC was sent instead of a CRS code. |
| The phone lights up but no text appears | iOS shows a Live Activity's alert on the activity, not as a notification. Send `device_token` so alerts also arrive as normal notifications. |
| The connection time never changes in the background | Register with `previous_service_uid` rather than a fixed `connection_minutes`. |
| Pushes stopped after a few hours | Registrations with nothing sent for six hours are removed. Arrival, or an APNs `410` for the token, also removes them. |
