CREATE TABLE profile (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    data JSON    NOT NULL DEFAULT '{}'
);
INSERT INTO profile (id, data) VALUES (1, '{"name":"Profile"}');

CREATE TABLE decision (
    profile    INTEGER NOT NULL REFERENCES profile(id) ON DELETE CASCADE,
    vacancy    INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    stage      TEXT    NOT NULL CHECK(stage IN ('shortlist','applied','dismissed')),
    reason     TEXT    NOT NULL DEFAULT '',
    decided_at TEXT    NOT NULL,
    PRIMARY KEY (profile, vacancy)
);

CREATE TABLE application (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    vacancy      INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    applied_at   TEXT    NOT NULL,
    profile      INTEGER NOT NULL,
    profile_name TEXT    NOT NULL
);

ALTER TABLE vacancy ADD COLUMN seen_at TEXT NOT NULL DEFAULT '';
