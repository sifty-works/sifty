# Collection

What the user gets from collection: vacancies from the sites they chose appear in the Inbox without any manual work, each once, with clean readable text, and nothing they removed comes back. Collection makes no model calls and no paid calls.

## Sources

A source is a name, a public http(s) address and an enabled switch. The user manages sources on the Sources page: a table of name, address, enabled switch and a remove action, and a form to add a source. The address is required. The address of an existing source cannot be edited; the user removes the source and adds it again.

Collection visits the enabled sources. Each source is recognised by its address:

| Kind of source | How it is read | Requests per run |
|---|---|---|
| Greenhouse, Lever, Ashby boards | the board's public JSON API, which includes descriptions | 1 |
| RSS or Atom feeds that carry descriptions (We Work Remotely) | the feed | 1 |
| Site-specific adapters for sites whose own data is broken (web3.career, Remote3) | the listing page, then the page of each new vacancy | 1 + new vacancies |
| Any other site | JSON-LD `JobPosting` on the listing page; if the answer is XML, RSS or Atom; otherwise links that look like vacancies and Getro `__NEXT_DATA__` | 1 + new vacancies without a description |

Only the first listing page is read; there is no pagination. Sites behind a login are not collected. A source that yields no vacancy over a full month is reported to the user on the Sources page as "No vacancies in 30 days", so that dead sources are visible and can be removed by hand.

The company of a Greenhouse vacancy is the company name the API gives, not the board's slug.

## When collection runs

- Collection runs on an interval, by default every 3 hours, measured from the start of the last run. The Sources page carries the switch that turns automatic collection on or off and the interval.
- Opening or reloading the application starts a run when the interval has passed and automatic collection is on.
- The "Collect jobs" button starts a run at once, ignoring the interval. It is disabled while a run is in progress.
- Only one run happens at a time.
- A run interrupted by a restart of the application is marked interrupted at the next start and never blocks the button.

## Politeness and safety of requests

- Only public http(s) addresses are requested. Private, loopback, link-local, multicast and unspecified addresses are refused before every request and on each of at most 6 redirects. Addresses with credentials are refused. A response body is limited to 8 MB. Every request has a timeout of 30 seconds. The User-Agent is `Sifty/1.0 (personal read-only job discovery)`.
- Requests to one domain are spaced by 3 seconds plus a random pause of up to 3 seconds. Sources on different domains are collected at the same time.
- After errors a domain is retried with an exponential pause from 30 seconds to 1 hour. A domain that answered with a block (403, or a captcha or Cloudflare page) is not requested again for 24 hours; this pause survives the end of the run and a restart.
- A 404 or 5xx answer is an error, never content: its body never becomes a description.
- When a domain blocks during a run, the remaining vacancies of that domain are still saved with the fields from the listing and the text state "Blocked by site".
- A vacancy's own page is requested only when the vacancy is new or its listing entry has no description and the stored vacancy has no text. A vacancy already stored with text is never requested again by collection.
- An address that matches a removed vacancy, or an item that matches an exclusion, is dropped before its page is requested.

## Fields and text

- Fields kept for a vacancy: title; company (`Unknown` when absent); original address, canonical; apply address, only when it points to one concrete vacancy in an applicant tracking system (Greenhouse, Lever, Ashby, Workable), never to the root of a board; location; tags; publication date with its basis, only when the source states a publication date (an update date is not accepted, a future date is dropped, a date once set is not replaced by another); expiry date (`validThrough`).
- A vacancy past its expiry date shows "Listing expired" with the ◷ mark and is not hidden.
- Any input text (HTML, escaped HTML, Markdown, plain text) becomes sanitised HTML limited to `h2 h3 h4 p ul ol li strong em br`. A paragraph that reads as a known section heading is shown as a heading. This sanitised HTML is the only stored form of the text; plain text and sections are derived from it when needed.
- On a vacancy's own page, JSON-LD is matched to exactly one listing item, by address or by a unique title and company pair; an ambiguous page is not guessed. Without JSON-LD, the sanitised body of the page is used (the content block, for site-specific adapters). A link to one concrete vacancy in an applicant tracking system found on an aggregator's page becomes the apply address.
- The vacancy page shows the text state in words: "Full text", "Listing summary only", "Blocked by site", "Not fetched".

## Industries

Industries replace the hard-coded Web3 mark and help to tell apart vacancies that share a title, such as a building architect and a software architect.

- There is one vocabulary of industries. Each industry has a name and a list of keywords. The application ships with a starting vocabulary (Software, Web3, Security, Agriculture, Architecture and construction, Photo and video, among others); the user edits it in Settings.
- When a vacancy is saved or its text changes, its industries are set by matching the keywords against its title, tags and text. A vacancy can have several industries or none.
- Industries are shown as badges in the Inbox row and on the vacancy page. How a profile uses them in search is described in `profiles.md` and `inbox.md`.
- Assigning industries makes no model call.

## New, changed, duplicate, removed

- A listing item matches a stored vacancy when (a) its canonical address was seen before, or (b) its apply address is the same concrete vacancy in an applicant tracking system, or (c) company and title, normalised, are the same, the company is not `Unknown`, the address is on another host, and the apply addresses do not conflict.
- **New:** the vacancy is saved with the stage New and appears in the Inbox of every profile whose search it fits. The open Inbox picks up new vacancies within 10 seconds.
- **Matched:** the last-seen date is updated. The title is always taken from the newer listing; company, location and tags when the newer value is not empty or `Unknown`. The text is replaced when it differs from the stored text and the new text is not worse: not empty and not blocked.
- **Several matches:** they are merged into the oldest vacancy. The strongest user state is kept (Applied over Shortlist over Dismissed over New), the user's notes, drafts and decisions follow, and the old id keeps pointing to the merged vacancy. Ids of merged and removed vacancies are never reused.
- **Removed for good:** the user removes a vacancy (outside triage, with confirmation, not during a run). Each of its addresses is remembered, and collection drops any later item with that address before requesting it.
- **Dismissed:** a dismissed vacancy stays dismissed; collection never brings it back as new. After 60 days unseen at its source its text is dropped and its row stays (see `settings-and-data.md`); if the source shows it again, its text is taken again and the dismissal stays.

## Exclusions

There is one list of exclusions, described in full in `inbox.md`. For collection it means: an item whose title or company matches an exclusion that applies to all profiles is saved with the listing's fields only, its page is not requested, it is not assessed and it is not shown. Removing the exclusion shows it again at once; its text is fetched at the next run that sees it.

## What the user sees of progress and errors

- During a run, the Sources page and the Inbox header show "Collecting… 5 of 14 sources".
- After a run, each source on the Sources page shows the time of its last success, the number of vacancies seen and the number of new ones, or the error of the last run in words ("Blocked by site", "Timed out", "Not found", "Nothing recognised on the page").
- A run where every source failed is shown as failed with a message in the Inbox header. A source that failed or yielded nothing in its last three runs is marked on the Sources page.

## Actions on a vacancy

- "Refresh source" on the vacancy page requests the vacancy's page again and replaces the text by the rule for matched vacancies. Opening a vacancy never makes a network request by itself.

## Not carried over

- The daily run at a fixed hour; the interval and the button replace it.
- Import of a single vacancy by address, and collection of a single source; neither had a way to be used.
- The jobspy source (LinkedIn, Indeed); it never worked.
- Automatic refetch of a vacancy when it is opened.
- A history of changes of a vacancy's text, and stored originals of fetched pages.
- The "Source coverage" page with its counters, and the seven runtime settings that had no effect.
- The Web3 mark as a fixed pattern; industries replace it.
- Sources that yield nothing: measured on a copy of the old data before switch-over, and those with no vacancy are not carried into the new configuration.

## Data from the old application

Vacancies cannot be collected again: sites show only live vacancies, and the stored ones carry the user's decisions. The import keeps every vacancy's id, title, company, addresses, location, dates, last-seen date and stage; its sanitised HTML passes through the new normaliser unchanged in meaning; aliases of merged vacancies and the next free id are kept; old exclusion words and blocked companies become exclusions. Removed vacancies were remembered as SHA-256 hashes of the old canonical address; the hashes are imported as they are, and the new application checks both the hash of an address under the old canonical rule and the new canonical address. Old run tables, page originals and section tables are not imported. Industries are assigned to every imported vacancy by the vocabulary at import, with no network or model call.
