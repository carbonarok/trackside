-- Live data is keyed by the stop's position (seq) in the service's schedule.
-- A schedule can change while the train runs (VSTP, or an overlay being
-- removed), shifting those positions, so the TIPLOC is kept too and used to
-- re-anchor the data when the two disagree.
ALTER TABLE service_events ADD COLUMN tiploc text;
ALTER TABLE darwin_forecasts ADD COLUMN tiploc text;
