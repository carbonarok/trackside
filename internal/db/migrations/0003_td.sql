-- SMART: maps Train Describer berth steps to arrivals and departures at
-- STANOX locations.
CREATE TABLE smart_berths (
    td          text NOT NULL,
    from_berth  text NOT NULL DEFAULT '',
    to_berth    text NOT NULL DEFAULT '',
    from_line   text,
    to_line     text,
    offset_secs integer NOT NULL DEFAULT 0,
    platform    text,
    event       char(1) NOT NULL,   -- A/C arrive (up/down), B/D depart (up/down)
    route       text,
    stanox      text NOT NULL,
    step_type   char(1) NOT NULL    -- B between, F from, T to, D intermediate, C clearout, I interpose
);
CREATE INDEX smart_berths_td_idx ON smart_berths (td);

-- Last known Train Describer position of each service.
ALTER TABLE services
    ADD COLUMN td_area     text,
    ADD COLUMN td_berth    text,
    ADD COLUMN td_berth_at timestamptz;

-- TD identifies trains by headcode only.
CREATE INDEX schedules_signalling_id_idx ON schedules (signalling_id);
