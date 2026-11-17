CREATE VIRTUAL TABLE vacancy_fts USING fts5(
    text,
    tokenize='trigram',
    content='',
    contentless_delete=1
);
