-- Searching for a train by headcode (1D78) looked through every schedule,
-- about 240 ms. With this index, and the search split so each half can use
-- an index, it takes a few milliseconds.
CREATE INDEX schedules_signalling_id_idx ON schedules (signalling_id);
