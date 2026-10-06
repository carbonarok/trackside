-- Darwin (National Rail) live data. Darwin identifies a train by its RID; we
-- map it onto our services by (train_uid, run_date).
ALTER TABLE services
    ADD COLUMN darwin_rid          text,
    ADD COLUMN darwin_cancel_code  integer,
    ADD COLUMN darwin_late_code    integer;
CREATE INDEX services_darwin_rid_idx ON services (darwin_rid) WHERE darwin_rid IS NOT NULL;

-- Per-location Darwin forecasts and actuals. Each Darwin update may carry
-- only some of these, so they are merged field by field.
CREATE TABLE darwin_forecasts (
    service_id         bigint NOT NULL REFERENCES services ON DELETE CASCADE,
    seq                integer NOT NULL,
    arr_et             timestamptz,
    arr_at             timestamptz,
    arr_delayed        boolean NOT NULL DEFAULT false,
    dep_et             timestamptz,
    dep_at             timestamptz,
    dep_delayed        boolean NOT NULL DEFAULT false,
    pass_et            timestamptz,
    pass_at            timestamptz,
    platform           text,
    platform_suppressed boolean NOT NULL DEFAULT false,
    platform_confirmed boolean NOT NULL DEFAULT false,
    arr_cancelled      boolean NOT NULL DEFAULT false,
    dep_cancelled      boolean NOT NULL DEFAULT false,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (service_id, seq)
);

-- Darwin reference data: reason texts and operator names.
CREATE TABLE darwin_reasons (
    kind text NOT NULL,                         -- late or cancel
    code integer NOT NULL,
    text text NOT NULL,
    PRIMARY KEY (kind, code)
);
CREATE TABLE operators (
    code text PRIMARY KEY,
    name text NOT NULL,
    url  text
);

-- Where a location's display name came from. Darwin's public station names
-- take precedence over names derived from CORPUS.
ALTER TABLE locations ADD COLUMN name_source text;
