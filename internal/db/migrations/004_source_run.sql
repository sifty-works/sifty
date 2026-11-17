CREATE TABLE source_run (
    job_id    INTEGER NOT NULL REFERENCES job(id) ON DELETE CASCADE,
    source    TEXT    NOT NULL,
    started   TEXT    NOT NULL,
    finished  TEXT    NOT NULL,
    status    TEXT    NOT NULL,
    found     INTEGER NOT NULL DEFAULT 0,
    new       INTEGER NOT NULL DEFAULT 0,
    updated   INTEGER NOT NULL DEFAULT 0,
    unchanged INTEGER NOT NULL DEFAULT 0,
    excluded  INTEGER NOT NULL DEFAULT 0,
    removed   INTEGER NOT NULL DEFAULT 0,
    error     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, source)
);
