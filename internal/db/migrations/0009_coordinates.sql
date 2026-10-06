-- Station coordinates (WGS84), from NaPTAN. Only passenger stations have
-- them; timing points such as junctions do not.
ALTER TABLE locations
    ADD COLUMN lat double precision,
    ADD COLUMN lon double precision;
