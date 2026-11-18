-- profile: each profile can carry a name and settings stored in data JSON.
-- A fresh database starts with one profile named "Profile".
CREATE TABLE profile (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    data JSON    NOT NULL DEFAULT '{}'
);
INSERT INTO profile (id, data) VALUES (1, '{"name":"Profile"}');

-- decision: one row per (profile, vacancy) that is not in Inbox.
-- No row for a (profile, vacancy) pair means the vacancy is in Inbox.
CREATE TABLE decision (
    profile    INTEGER NOT NULL REFERENCES profile(id) ON DELETE CASCADE,
    vacancy    INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    stage      TEXT    NOT NULL CHECK(stage IN ('shortlist','applied','dismissed')),
    reason     TEXT    NOT NULL DEFAULT '',
    decided_at TEXT    NOT NULL,
    PRIMARY KEY (profile, vacancy)
);

-- application: written when the user confirms a job application.
-- profile is stored without a foreign key so the history survives profile deletion.
CREATE TABLE application (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    vacancy      INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    applied_at   TEXT    NOT NULL,
    profile      INTEGER NOT NULL,
    profile_name TEXT    NOT NULL
);

-- seen_at on vacancy: empty when unread, set once for all profiles.
ALTER TABLE vacancy ADD COLUMN seen_at TEXT NOT NULL DEFAULT '';
