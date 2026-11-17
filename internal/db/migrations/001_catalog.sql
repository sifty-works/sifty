CREATE TABLE vacancy (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    identity       TEXT    NOT NULL,
    title          TEXT    NOT NULL,
    company        TEXT    NOT NULL,
    url            TEXT    NOT NULL UNIQUE,
    apply_url      TEXT    NOT NULL DEFAULT '',
    location       TEXT    NOT NULL DEFAULT '',
    tags           TEXT    NOT NULL DEFAULT '[]',   -- JSON array
    source_fields  TEXT    NOT NULL DEFAULT '{}',   -- JSON object
    published_at   TEXT    NOT NULL DEFAULT '',     -- UTC RFC 3339 or empty
    published_basis TEXT   NOT NULL DEFAULT '',
    expires_at     TEXT    NOT NULL DEFAULT '',     -- UTC RFC 3339 or empty
    first_seen     TEXT    NOT NULL,                -- UTC RFC 3339
    last_seen      TEXT    NOT NULL,                -- UTC RFC 3339
    text_state     TEXT    NOT NULL DEFAULT 'not_fetched',
    content_hash   TEXT    NOT NULL DEFAULT '',     -- SHA-256 hex of plain text
    version        INTEGER NOT NULL DEFAULT 1,
    body           BLOB    NOT NULL DEFAULT ''      -- sanitised HTML compressed with compress/flate
);

CREATE INDEX vacancy_identity  ON vacancy (identity);
CREATE INDEX vacancy_apply_url ON vacancy (apply_url) WHERE apply_url != '';

CREATE TABLE vacancy_source (
    vacancy_id INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    source     TEXT    NOT NULL,
    url        TEXT    NOT NULL UNIQUE,
    seen_at    TEXT    NOT NULL,
    PRIMARY KEY (vacancy_id, source, url)
);

CREATE TABLE vacancy_alias (
    old_id INTEGER PRIMARY KEY,
    new_id INTEGER NOT NULL REFERENCES vacancy(id)
);

CREATE TABLE tombstone (
    key TEXT PRIMARY KEY  -- "url:<canonical address>" or "legacy:<hex>"
);
