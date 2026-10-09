# Getting the data

trackside serves nothing until it has a timetable, and live running needs
feed credentials. Both data providers approve accounts by hand, so request
access first and set up everything else while you wait.

- [Which feeds you need](#which-feeds-you-need)
- [Two routes to the same data](#two-routes-to-the-same-data)
- [Network Rail open data account](#network-rail-open-data-account)
- [Rail Data Marketplace account](#rail-data-marketplace-account)
- [Darwin Lite](#darwin-lite)
- [Automatic file delivery (inbox)](#automatic-file-delivery-inbox)
- [How often each file needs loading](#how-often-each-file-needs-loading)

## Which feeds you need

| Feed | What it gives you | Where it comes from | Needed? |
|---|---|---|---|
| SCHEDULE | The timetable | Network Rail, or the Rail Data Marketplace as a file | **Yes** |
| CORPUS | Station and location names and codes | Network Rail, or the Rail Data Marketplace as a file | **Yes** |
| TRUST (Train Movements) | Live actual times, cancellations | Network Rail or the Rail Data Marketplace | Recommended |
| VSTP | Trains added at short notice | Network Rail or the Rail Data Marketplace | Recommended |
| Darwin Push Port | Forecasts, live platforms, delay reasons | Rail Data Marketplace | Recommended |
| Darwin Lite (OpenLDBWS) | The same forecasts, on demand, one station at a time | National Rail | Optional, if you lack the Push Port |
| Darwin reference data | Proper station names, reason texts, complete station codes | Rail Data Marketplace (file) | Recommended with Darwin |
| TD (Train Describer) + SMART | Earlier times, train positions | Network Rail or the Rail Data Marketplace | Optional |
| NaPTAN | Station coordinates for the map | Department for Transport | Automatic, no account |

With only a Network Rail account you get the full timetable and live running,
with trackside estimating times from reported delays. Adding Darwin gives you
the same forecasts and platforms that station screens show.

**Load the timetable before expecting live data.** Live messages are matched
against the timetable, so until SCHEDULE and CORPUS are loaded every board is
empty and the API answers `unknown location`.

## Two routes to the same data

Network Rail's feeds (SCHEDULE, CORPUS, SMART, TRUST, VSTP, TD) come from
either:

- **Network Rail's own platform**, over STOMP, with files you can download
  on demand. One username and password covers everything. The platform is
  capped at 1,000 users, and new accounts can take days to be activated.
- **The Rail Data Marketplace**, over Kafka, with files delivered to storage
  you provide. Every product is a separate subscription with its own
  credentials. Darwin is only available here.

You can mix them per feed. Any feed with `RDM_<FEED>_*` set is read from
Kafka, and the rest from Network Rail. If Network Rail's platform is full or
your account isn't activated yet, the Rail Data Marketplace alone is enough:
subscribe to every product below and use the [inbox](#automatic-file-delivery-inbox)
for the files.

## Network Rail open data account

1. **Register at [publicdatafeeds.networkrail.co.uk](https://publicdatafeeds.networkrail.co.uk/).**
   The [Open Rail Data wiki](https://wiki.openraildata.com/index.php/About_the_Network_Rail_feeds)
   documents the process. The wiki's own "Request account" link only gives
   you a login for editing the wiki, not data access.
2. **Set your password.** You'll get a confirmation email. Log in and change
   your password. It must be at least 8 characters and include upper case,
   lower case, a number and a special character. The site may show an
   unhelpful error if it doesn't meet these rules.
3. **Wait for activation.** Network Rail emails you when the account is
   active, which can take several days. Until then every request is refused
   with exactly the same errors as a wrong password (see
   [Troubleshooting](running.md#troubleshooting)).
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

## Rail Data Marketplace account

1. **Register at [raildata.org.uk](https://raildata.org.uk).** New accounts
   are approved by hand. Expect anything from a few hours to a few days.
2. **Subscribe to each product you want.** All of them are free. Each is a
   separate subscription that renews yearly. Search for these exact names
   (the search box's suggestions sometimes jump to the wrong product; TD is
   easiest to find by searching "Describer"):

   | Product | Kind | trackside setting |
   |---|---|---|
   | Darwin Real Time Train Information | Kafka | `RDM_DARWIN_*` |
   | NWR Train Movements | Kafka | `RDM_TRUST_*` |
   | NWR Very Short-Term Planning (VSTP) | Kafka | `RDM_VSTP_*` |
   | NWR Train Describer (TD) | Kafka | `RDM_TD_*` |
   | NWR Schedule | File | inbox or `import-schedule FILE` |
   | NWR CORPUS | File | inbox or `import-corpus FILE` |
   | NWR SMART | File | inbox or `import-smart FILE` |
   | Darwin Timetable Files | File | inbox or `import-darwin-ref FILE` |

   "SMART Berth Offsets" is a CSV product that trackside can't read; use
   **NWR SMART**.
3. **Copy each Kafka product's connection details.** Open the product's
   **Pub/Sub** tab and copy the consumer key, consumer secret and consumer
   group into `.env`:

   ```bash
   RDM_DARWIN_USERNAME=<consumer key>
   RDM_DARWIN_PASSWORD=<consumer secret>
   RDM_DARWIN_GROUP=<consumer group, e.g. SC-1234abcd-...>
   ```

   Every product has its own key, secret and group, and you must use the
   group issued for that product. Any other group ID is refused with
   `GroupAuthorizationFailed`. The topic names on the Pub/Sub tab should
   match trackside's defaults (see [Configuration](../README.md#configuration));
   if the bootstrap server shown differs from the default, set
   `RDM_BOOTSTRAP` too.

> [!IMPORTANT]
> **One running copy per set of credentials.** A Kafka consumer group shares
> its messages out between everyone connected to it. If a second trackside
> starts with the same `RDM_*` settings (a test run on your laptop, a second
> replica), each copy silently gets only part of every feed. trackside reads
> `.env` from the directory it starts in, so running `trackside serve` in a
> folder that holds your server's `.env` is enough to do this.

Two quirks of the marketplace website:

- **Opening a product page by its URL can sign you out.** Navigate to it by
  clicking through from the catalogue instead.
- **A product's Data files tab is empty until you subscribe.**

**Downloading files by hand.** Each file product's **Data files** tab lists
the latest files. Download them and load them with `import-schedule FILE`,
`import-corpus FILE`, `import-smart FILE` and `import-darwin-ref FILE`. The
full timetable is `CIF_ALL_FULL_DAILY_toc-full.json.gz` (about 140 MB). The
Darwin reference file is the newest `*_ref_v4.xml.gz`. To keep the timetable
current without doing this daily, use the [inbox](#automatic-file-delivery-inbox).

**Darwin reference data matters more than its name suggests.** Without it,
station names come from CORPUS in capitals ("CLAPHAM JUNCTION"), and some
large stations with several TIPLOCs show only part of their trains, because
only Darwin's reference data maps every TIPLOC to the station's CRS code.

## Darwin Lite

If you don't have the Darwin Push Port, or are still waiting for approval, a
**Darwin Lite** token gives you the same Darwin forecasts on demand. Darwin
Lite is National Rail's OpenLDBWS (Live Departure Boards Web Service).

**There is no login.** Register at
[realtime.nationalrail.co.uk/OpenLDBWSRegistration](https://realtime.nationalrail.co.uk/OpenLDBWSRegistration/).
The token in the registration email is the whole credential, and old tokens
keep working:

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
- **Live updates:** boards built from Darwin Lite are never pushed over the
  WebSocket, so clients are told to poll every 30 seconds instead of 120.
- **Precedence:** if the Darwin Push Port is configured too, it takes over and
  Darwin Lite isn't used.

## Automatic file delivery (inbox)

The Rail Data Marketplace doesn't let you download files on demand from a
program. It pushes them to storage you provide, on its own schedule.
trackside can watch that storage and import whatever arrives:

- **Which files:** CORPUS, SMART, Darwin reference data and SCHEDULE.
  Subfolders are searched too, since some products deliver into their own
  folder.
- **Timetable order:** updates are applied in sequence. If there's no
  timetable yet, or an update is missing, the newest full file is used.
- **Bookkeeping:** each file is imported once. CIF-format copies and
  unrelated files are ignored. Gzip is detected from the content, since some
  files arrive compressed without a `.gz` name.
- **When:** every 15 minutes (`INBOX_INTERVAL`), or on demand with
  `trackside import-inbox`.

To set up delivery in the marketplace: **Manage → My file transfers → Add
file destination**, then on each file product's **Data files → File
transfers** tab, choose that destination. The marketplace can deliver to
Amazon S3, Google Cloud Storage, Azure Blob Storage or an SFTP server.

### Google Cloud Storage (free)

**Google Cloud Storage is the free option.** Its Always Free tier gives 5 GB
of storage and 100 GB of downloads a month with no time limit, though it
needs a billing account (a card) on the Google Cloud project.

1. **Create the bucket.** In the [Google Cloud console](https://console.cloud.google.com/storage),
   create a bucket: Region `us-central1`, `us-east1` or `us-west1` (the free
   tier regions), Standard storage class.
2. **Stop it filling up.** Under the bucket's **Lifecycle** tab, add a rule
   to delete objects older than 7 days.
3. **Add it as a destination in the marketplace.** Choose Google Cloud Storage
   and enter the bucket name. The form shows the marketplace's service
   accounts. On the bucket's **Permissions** tab, grant them Storage Object
   Viewer, Storage Legacy Bucket Reader, Storage Bucket Viewer and Storage
   Legacy Bucket Writer. Then **Validate** and **Submit**.
4. **Point each product at it:** NWR Schedule, NWR CORPUS, NWR SMART and
   Darwin Timetable Files.
5. **Give trackside read access.** Under **Cloud Storage → Settings →
   Interoperability**, create an HMAC key for a service account that has
   Storage Object Viewer on the bucket. Then:

   ```bash
   INBOX_BUCKET=gs://your-bucket
   INBOX_ACCESS_KEY=GOOG1E...
   INBOX_SECRET_KEY=...
   ```

### Your own SFTP server

If the marketplace delivers to an SFTP server you run, trackside can fetch
from it. Pin the server's host key, which `ssh-keyscan -p PORT HOST` prints:

```bash
INBOX_SFTP=sftp://rdm@sftp.example.com:2222/upload
INBOX_SFTP_PASSWORD=...
INBOX_SFTP_HOST_KEY="ssh-ed25519 AAAA..."
```

Running the SFTP server for the marketplace to deliver to has some catches:

- **Password login only.** The destination form has no field for a key.
- **It uses an old SSH client.** A current OpenSSH refuses it with `no
  matching key exchange method`. Allow it in that server's `sshd_config`:

  ```
  HostKey /etc/ssh/ssh_host_ecdsa_key
  KexAlgorithms +ecdh-sha2-nistp256,ecdh-sha2-nistp384,ecdh-sha2-nistp521,diffie-hellman-group-exchange-sha256
  MACs +hmac-sha2-256,hmac-sha2-512
  ```

  It also needs an ECDSA host key, which the `HostKey` line above provides.
- **It must be reachable from the internet.** The form's **Validate** button
  is the real test. The marketplace connects from Google Cloud; its support
  team will give you the address list if you want to allow only those.

Keep that server separate from the machine trackside runs on, or at least
give the delivery user nothing but its upload folder.

### A local folder

For SFTP delivery to the machine trackside runs on, or files you download by
hand, point `INBOX_DIR` at the folder instead. A dedicated folder is best,
but a busy one such as Downloads also works, because unrecognised files are
ignored. In Docker, mount the folder into the container and set `INBOX_DIR`
to the path inside it.

## How often each file needs loading

| File | How often | Done by |
|---|---|---|
| SCHEDULE full | Once, then only to recover from a missed update | `import-schedule`, or the inbox |
| SCHEDULE update | Daily | `serve` from about 06:00 with Network Rail credentials, or the inbox |
| CORPUS | Occasionally (new stations) | `import-corpus`, or the inbox |
| SMART | Occasionally (signalling changes) | `import-smart`, or the inbox |
| Darwin reference data | Occasionally | `import-darwin-ref`, or the inbox |
| NaPTAN | Weekly | `serve`, automatically |
