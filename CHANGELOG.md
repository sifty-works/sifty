# Changelog

Notable changes of Sifty, newest first. Versions follow `MAJOR.MINOR.PATCH`.

## Unreleased

- Vacancy texts no longer run words together where the source wrapped a space in bold or italics, and keep no empty bold or italic marks.
- Settings → Sources: add, switch off and remove sources, set automatic collection and its interval, and start a collection with "Collect jobs". Collection reads Greenhouse, Lever and Ashby boards and RSS or Atom feeds, shows its progress in the header, and new vacancies appear in the Inbox without reloading the page.
- The Inbox lists the stored vacancies, newest first, with company, title, location and publication date, and searches title, company, tags and text on the server (`/` focuses the search field).
- `sifty import-v1` imports the vacancies, the places they were seen, merged ids, removed addresses, the sources that still had vacancies and the time of the last collection from the previous version.
- The settings file holds the collection switch, the interval (15 minutes to 7 days) and the sources, and keeps its comments when the application changes it.
- `sifty serve` starts on an empty data folder without any setup and refuses a second copy on the same folder; opening it shows an empty Inbox with a hint to add sources; `sifty version` and `sifty config defaults`.
