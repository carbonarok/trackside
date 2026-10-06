-- Timetable associations: trains that join (JJ), divide (VV) or form the
-- next working (NP). Dates and days apply to the main train; date_indicator
-- says whether the associated train runs the same day (S), the next (N) or
-- the previous (P).
CREATE TABLE associations (
    id             bigserial PRIMARY KEY,
    main_uid       text NOT NULL,
    assoc_uid      text NOT NULL,
    start_date     date NOT NULL,
    end_date       date NOT NULL,
    days_runs      text NOT NULL,
    category       text,
    date_indicator text,
    tiploc         text NOT NULL,
    stp            char(1) NOT NULL,
    UNIQUE (main_uid, assoc_uid, start_date, tiploc, stp)
);
CREATE INDEX associations_main_idx ON associations (main_uid);
CREATE INDEX associations_assoc_idx ON associations (assoc_uid);

-- Darwin's live associations, keyed by Darwin train IDs (RIDs).
CREATE TABLE darwin_associations (
    main_rid   text NOT NULL,
    assoc_rid  text NOT NULL,
    tiploc     text NOT NULL,
    category   text NOT NULL,
    cancelled  boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (main_rid, assoc_rid, tiploc, category)
);
CREATE INDEX darwin_associations_assoc_idx ON darwin_associations (assoc_rid);
