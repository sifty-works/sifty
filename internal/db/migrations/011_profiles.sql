-- me: My data as one JSON record, shared by all profiles.
CREATE TABLE me (
    id   INTEGER PRIMARY KEY,
    data JSON    NOT NULL DEFAULT '{}'
);
INSERT INTO me (id, data) VALUES (1, '{}');

-- settings: key-value store for application state (active profile, etc.).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- qa: Questions and Answers, shared by all profiles.
CREATE TABLE qa (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    question   TEXT    NOT NULL,
    answer     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

-- fix: rename the default profile from "Profile" to "Profile 1".
UPDATE profile
SET    data = json_set(data, '$.name', 'Profile 1')
WHERE  id = 1
  AND  json_extract(data, '$.name') = 'Profile';
