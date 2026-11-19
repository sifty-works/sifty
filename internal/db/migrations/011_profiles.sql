CREATE TABLE me (
    id   INTEGER PRIMARY KEY,
    data JSON    NOT NULL DEFAULT '{}'
);
INSERT INTO me (id, data) VALUES (1, '{}');

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE qa (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    question   TEXT    NOT NULL,
    answer     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

UPDATE profile
SET    data = json_set(data, '$.name', 'Profile 1')
WHERE  id = 1
  AND  json_extract(data, '$.name') = 'Profile';
