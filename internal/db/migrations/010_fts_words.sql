DROP TABLE IF EXISTS vacancy_fts;
CREATE VIRTUAL TABLE vacancy_fts USING fts5(
    title,
    tags,
    text,
    tokenize='trigram',
    content='',
    contentless_delete=1
);
