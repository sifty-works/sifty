CREATE TABLE exclusion (
    id         INTEGER PRIMARY KEY,
    kind       TEXT    NOT NULL CHECK(kind IN ('word', 'company')),
    value      TEXT    NOT NULL,
    norm       TEXT    NOT NULL,
    profile_id INTEGER NOT NULL DEFAULT 0,
    created    TEXT    NOT NULL,
    UNIQUE(kind, norm, profile_id)
);
