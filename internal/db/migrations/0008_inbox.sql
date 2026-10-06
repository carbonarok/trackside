-- Files picked up from an inbox (a folder or bucket that the Rail Data
-- Marketplace delivers to). A file is identified by name, size and
-- modification time, so a re-delivered file with new content is processed
-- again.
CREATE TABLE inbox_files (
    name         text NOT NULL,
    size         bigint NOT NULL,
    modified     timestamptz NOT NULL,
    kind         text NOT NULL,
    result       text NOT NULL,          -- loaded, skipped or an error
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (name, size, modified)
);
