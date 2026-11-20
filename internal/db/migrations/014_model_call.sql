CREATE TABLE model_call (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    background INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    chars_in INTEGER NOT NULL,
    tokens_in INTEGER NOT NULL,
    chars_out INTEGER NOT NULL,
    tokens_out INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    vacancy_id INTEGER
);
