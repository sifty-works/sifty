CREATE TABLE vacancy_reading (
    vacancy_id     INTEGER NOT NULL REFERENCES vacancy(id) ON DELETE CASCADE,
    version        INTEGER NOT NULL,
    reader_version TEXT    NOT NULL,
    record         TEXT    NOT NULL,
    created        TEXT    NOT NULL,
    PRIMARY KEY (vacancy_id)
);
