-- The station a train is approaching, from Train Describer: set when it
-- steps into the berth before the station, cleared when it arrives.
ALTER TABLE services
    ADD COLUMN td_approach_tiploc text,
    ADD COLUMN td_approach_at     timestamptz;
