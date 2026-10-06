-- Archived performance of every public call, kept after the live tables
-- move on. Times are seconds after midnight of the run date (booked) or
-- timestamps (actual). One row per train per public stop.
CREATE TABLE history_stops (
    run_date      date NOT NULL,
    train_uid     text NOT NULL,
    seq           integer NOT NULL,
    headcode      text,
    atoc_code     text,
    tiploc        text NOT NULL,
    crs           text,
    gbtt_arr      integer,
    gbtt_dep      integer,
    actual_arr    timestamptz,
    actual_dep    timestamptz,
    arr_source    text,
    dep_source    text,
    arr_cancelled boolean NOT NULL DEFAULT false,
    dep_cancelled boolean NOT NULL DEFAULT false,
    cancel_code   text,
    cancel_reason text,
    late_reason   text,
    PRIMARY KEY (run_date, train_uid, seq)
);
CREATE INDEX history_stops_crs_idx ON history_stops (crs, run_date);
CREATE INDEX history_stops_uid_idx ON history_stops (train_uid, run_date);
