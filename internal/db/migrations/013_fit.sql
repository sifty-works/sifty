-- fit: Fits you of one vacancy for one profile, stored because the list sorts
-- and filters on it. version is the vacancy version and inputs the hash of the
-- profile's inputs it was computed from; a row is current while both match.
-- listing is a hash of the version and the listing fields read besides the
-- text (title, location, tags, source fields); the background recomputation
-- also recomputes a row whose listing hash changed.
-- score is the rounded number, NULL unless state is 'scored'.
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
