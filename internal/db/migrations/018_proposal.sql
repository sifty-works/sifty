CREATE TABLE proposal (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES document(id) ON DELETE CASCADE,
    field       TEXT    NOT NULL,
    value       TEXT    NOT NULL,
    quote       TEXT    NOT NULL,
    to_review   INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT    NOT NULL
);
