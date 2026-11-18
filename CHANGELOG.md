# Changelog

Notable changes of Sifty, newest first. Versions follow `MAJOR.MINOR.PATCH`.

## Unreleased

- Collection also skips the page of a vacancy on the Greenhouse board of a blocked company, as the lists already hid it.
- A source whose site does not answer shows "Site not answering"; "Timed out" is kept for a request that ran out of time.
- Settings → Exclusions: words and companies that hide vacancies everywhere, with "Would hide N roles" while typing; "Block company" on an Inbox row. Collection skips the page of an excluded vacancy, and removing an exclusion shows its vacancies at once. The import brings the old stop words and blocked companies.
- A new vacancy found without a description gets its text, company and location from its own page; a page that could not be read is tried again after 7 days, and a site that blocks reading marks the vacancy "Blocked by site".
- Any other site is read too: job postings described on its page, its feed, a Getro board, or links that look like vacancies; We Work Remotely and Remote3 are read through their feeds and web3.career through its vacancy links.
- A site that blocked Sifty is left alone for 24 hours even across a restart.
- Each source on the Sources page shows its last success with vacancies seen and new, or the error of its last run in words, or "No vacancies in 30 days"; sources failing three runs in a row are marked, and the header shows "N sources need attention".
- A role that moves from one applicant tracking system to another (same company and title) stays one vacancy; a board's name taken from its address no longer replaces a known company name.
- Vacancy texts no longer run words together where the source wrapped a space in bold or italics, and keep no empty bold or italic marks.
- Settings → Sources: add, switch off and remove sources, set automatic collection and its interval, and start a collection with "Collect jobs". Collection reads Greenhouse, Lever and Ashby boards and RSS or Atom feeds, shows its progress in the header, and new vacancies appear in the Inbox without reloading the page.
- The Inbox lists the stored vacancies, newest first, with company, title, location and publication date, and searches title, company, tags and text on the server (`/` focuses the search field).
- `sifty import-v1` imports the vacancies, the places they were seen, merged ids, removed addresses, the sources that still had vacancies and the time of the last collection from the previous version.
- The settings file holds the collection switch, the interval (15 minutes to 7 days) and the sources, and keeps its comments when the application changes it.
- `sifty serve` starts on an empty data folder without any setup and refuses a second copy on the same folder; opening it shows an empty Inbox with a hint to add sources; `sifty version` and `sifty config defaults`.
