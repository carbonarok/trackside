-- Station boards look up every public call at a station's TIPLOCs. With
-- only the tiploc index, a busy station's rows (56,000 at Clapham
-- Junction) are fetched one by one from all over the table. This index
-- carries what the board needs, for public calls only, so boards read it
-- alone: at Clapham Junction, 290 pages instead of 16,800.
CREATE INDEX schedule_locations_board_idx ON schedule_locations (tiploc)
    INCLUDE (schedule_id, seq, gbtt_arr, gbtt_dep)
    WHERE gbtt_arr IS NOT NULL OR gbtt_dep IS NOT NULL;
