# Contributing

Bug reports and pull requests are welcome. trackside is young and has only
run against the live feeds on a handful of instances, so reports of trains
that look wrong are especially useful: include the station, the train's UID
or headcode, the date, and what official sources showed instead.

## Development setup

You need Go 1.25, Node 22 and Postgres 14 or later.

```bash
make build                  # builds the website into web/dist, then the binary
make test                   # Go unit tests and a type check of the website
make dev                    # the website with hot reload on :5173, using a trackside on :8080
```

The end-to-end tests need a database they're allowed to wipe:

```bash
createdb trackside_test
TEST_DATABASE_URL=postgres://localhost/trackside_test go test ./...
```

Without `TEST_DATABASE_URL` the database tests are skipped, not failed, so
run them before sending anything that touches SQL or migrations. CI always
runs them.

**Don't develop with your server's feed credentials.** trackside reads `.env`
from the directory it starts in. A `trackside serve` started with the same
Rail Data Marketplace credentials as a running server joins its Kafka
consumer groups and takes part of every feed away from it. Use a separate
`.env` with no feed credentials, or separate subscriptions, for development.
Most work needs no live feeds at all: the files in `testdata/` cover every
message format.

## Before you send a pull request

CI runs these, so run them first:

```bash
gofmt -l .                  # must print nothing
go vet ./...
go test -race ./...         # with TEST_DATABASE_URL set
cd web && npx tsc -b
```

Some things to know:

- **The API spec is tested.** `internal/api/openapi.yaml` must list exactly
  the fields the response structs have. If you change a response, change the
  spec too.
- **Keep Go at 1.25.** The `go` line in `go.mod` must match the Dockerfile's
  `golang:1.25` image. `go get -u` or `go get pkg@latest` can quietly raise it
  and break the Docker build; check `go.mod` after updating dependencies.
- **Migrations only go forwards.** Add a new numbered file in
  `internal/db/migrations/`; never edit one that has been released. They run
  automatically on start.
- **Sample data:** new message handling should come with a sample in
  `testdata/` in the real feed format, taken from the published
  documentation or a real message with nothing personal in it.
- **Never commit credentials.** `.env` is ignored by git. Don't paste real
  tokens, consumer groups or keys into issues, tests or logs.

## Licence

By contributing you agree that your contribution is licensed under the
[GNU Affero General Public License v3.0](LICENSE), like the rest of
trackside.
