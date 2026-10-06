-- Reference data: one row per TIPLOC, merged from CORPUS and the SCHEDULE
-- feed's TiplocV1 records.
CREATE TABLE locations (
    tiploc          text PRIMARY KEY,
    stanox          text,
    crs             text,
    nlc             text,
    name            text,
    tps_description text
);
CREATE INDEX locations_crs_idx ON locations (crs) WHERE crs IS NOT NULL;
CREATE INDEX locations_stanox_idx ON locations (stanox) WHERE stanox IS NOT NULL;

-- Timetable schedules from the SCHEDULE (CIF) and VSTP feeds. The natural key
-- is (train_uid, start_date, stp) within each source; CIF and VSTP schedules
-- are kept apart so a full CIF reload never collides with a VSTP schedule.
CREATE TABLE schedules (
    id                   bigserial PRIMARY KEY,
    train_uid            text NOT NULL,
    start_date           date NOT NULL,
    end_date             date NOT NULL,
    days_runs            text NOT NULL,          -- 7 chars, Monday first
    stp                  char(1) NOT NULL,       -- C, N, O or P
    source               char(1) NOT NULL,       -- C = CIF, V = VSTP
    bank_holiday_running text,
    train_status         text,
    signalling_id        text,
    category             text,
    power_type           text,
    train_class          text,
    speed                integer,
    atoc_code            text,
    service_code         text,
    UNIQUE (train_uid, start_date, stp, source)
);
CREATE INDEX schedules_range_idx ON schedules (start_date, end_date);

-- Times are seconds after midnight of the run date, so a train crossing
-- midnight has values >= 86400. All times are UK local wall-clock times.
CREATE TABLE schedule_locations (
    schedule_id bigint NOT NULL REFERENCES schedules ON DELETE CASCADE,
    seq         integer NOT NULL,
    tiploc      text NOT NULL,
    loc_type    text NOT NULL,                  -- LO, LI or LT
    wtt_arr     integer,
    wtt_dep     integer,
    wtt_pass    integer,
    gbtt_arr    integer,
    gbtt_dep    integer,
    platform    text,
    line        text,
    path        text,
    PRIMARY KEY (schedule_id, seq)
);
CREATE INDEX schedule_locations_tiploc_idx ON schedule_locations (tiploc);

-- One row per train per run date: the schedule that wins STP resolution, plus
-- live state from TRUST.
CREATE TABLE services (
    id             bigserial PRIMARY KEY,
    train_uid      text NOT NULL,
    run_date       date NOT NULL,
    schedule_id    bigint REFERENCES schedules ON DELETE SET NULL,
    planned_cancel boolean NOT NULL DEFAULT false,
    trust_id       text,
    activated_at   timestamptz,
    cancel_stanox  text,
    cancel_type    text,
    cancel_reason  text,
    origin_stanox  text,                        -- set by a TRUST change of origin
    UNIQUE (train_uid, run_date)
);
CREATE INDEX services_run_date_idx ON services (run_date);
CREATE INDEX services_schedule_id_idx ON services (schedule_id, run_date);
CREATE INDEX services_trust_id_idx ON services (trust_id) WHERE trust_id IS NOT NULL;

-- Realtime events, matched to a schedule location by seq.
CREATE TABLE service_events (
    service_id bigint NOT NULL REFERENCES services ON DELETE CASCADE,
    seq        integer NOT NULL,
    event      text NOT NULL,                   -- arr, dep or pass
    actual     timestamptz NOT NULL,
    platform   text,
    source     text NOT NULL,
    PRIMARY KEY (service_id, seq, event)
);

-- Cursor and bookkeeping for each feed.
CREATE TABLE feed_state (
    feed       text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
