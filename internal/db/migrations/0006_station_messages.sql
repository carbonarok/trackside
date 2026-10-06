-- Darwin station messages: disruption and information notices shown on
-- station departure screens. A message replaces any earlier one with the
-- same id; a message listing no stations removes it.
CREATE TABLE station_messages (
    id         integer PRIMARY KEY,
    category   text NOT NULL,
    severity   integer NOT NULL,
    suppress   boolean NOT NULL DEFAULT false,
    html       text NOT NULL,
    text       text NOT NULL,
    stations   text[] NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX station_messages_stations_idx ON station_messages USING gin (stations);
