# Changelog

Notable changes of Sifty, newest first. Versions follow `MAJOR.MINOR.PATCH`.

## Unreleased

- The Inbox lists the stored vacancies, newest first, with company, title, location and publication date, and searches title, company, tags and text on the server (`/` focuses the search field).
- `sifty import-v1` imports the vacancies, the places they were seen, merged ids, removed addresses, the sources that still had vacancies and the time of the last collection from the previous version.
- The settings file holds the collection switch, the interval (15 minutes to 7 days) and the sources, and keeps its comments when the application changes it.
- `sifty serve` starts on an empty data folder without any setup and refuses a second copy on the same folder; opening it shows an empty Inbox with a hint to add sources; `sifty version` and `sifty config defaults`.
