CREATE TABLE fit (
    profile INTEGER NOT NULL REFERENCES profile(id) ON DELETE CASCADE,
    vacancy INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    inputs  INTEGER NOT NULL,
    listing INTEGER NOT NULL,
    state   TEXT    NOT NULL,
    score   INTEGER,
    PRIMARY KEY (profile, vacancy)
) WITHOUT ROWID;
