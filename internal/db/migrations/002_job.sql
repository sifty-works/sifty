CREATE TABLE job (
    id         INTEGER PRIMARY KEY,
    kind       TEXT    NOT NULL,
    subject    TEXT    NOT NULL,
    priority   INTEGER NOT NULL DEFAULT 0,
    state      TEXT    NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    not_before TEXT    NOT NULL,
    progress   TEXT    NOT NULL DEFAULT '{}',
    error      TEXT    NOT NULL DEFAULT '',
    params     TEXT,
    created    TEXT    NOT NULL,
    updated    TEXT    NOT NULL
);

CREATE INDEX job_dispatch ON job (state, not_before, priority DESC, created ASC);
