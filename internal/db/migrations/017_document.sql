CREATE TABLE document (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK(kind IN ('pdf','docx','md','txt','text')),
    state        TEXT    NOT NULL,
    state_reason TEXT    NOT NULL DEFAULT '',
    char_count   INTEGER NOT NULL DEFAULT 0,
    evidence     INTEGER NOT NULL DEFAULT 0,
    text         TEXT    NOT NULL DEFAULT '',
    file_name    TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL
);
