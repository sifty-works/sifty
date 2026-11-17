# Profiles and My data

What the user gets: one set of facts about themselves (My data), several profiles that each describe one kind of job they look for, and documents (CVs, notes, pages) from which a model proposes facts that the user accepts or rejects. Nothing the user did not accept is used as a fact, and saving any change recomputes assessment in the background without a model call.

## Who owns which field

- **My data**, one for all profiles: display name, legal name, contacts, photo, application form answers, skills, skills the user does not have, Confirmed by you, experience, languages, availability, current location, work authorizations.
- **A profile**: name, headline, target roles, search keywords, exclusions of this profile (see `inbox.md`), target industries, compensation, engagement, hours, work modes, preferred locations, relocation, time zones, other requirements.
- An empty field means unknown, never zero or false.

## Profile switcher

- The header button shows a user icon in the profile's colour and its name, "Profile 1" by default. The colour comes from a fixed palette by the profile's position in the list and is not stored.
- The "Profiles" menu lists every profile with its colour dot and name, and a check at the active one. Clicking the active profile opens its page; clicking another makes it active and reloads the lists, with no model call.
- "Rename" edits the name in place: Save or Enter keeps it, Escape cancels, an empty name is ignored.
- "+ Add new profile" asks for a name (Create is disabled while it is empty). The new profile gets the next colour, becomes active and opens. It starts empty: no decisions are copied, and its empty goals are inherited from the first profile.
- "Delete profile…" asks for confirmation and removes the profile with its stages, decisions and exclusions. Applications made from it stay recorded. The first profile cannot be deleted, because other profiles inherit from it.
- A click outside the menu or Escape closes it.

## Profile page header

- **Photo.** Clicking the avatar uploads a png, jpg or webp image of at most 2 MiB; the server checks the real image type. "Remove photo" deletes it. Without a photo the avatar shows the two initials of the display name, or an icon. The caption reads "Photo attached when an application form asks for one" or "No photo · click the avatar to add one…".
- **Name.** The title is the display name ("Your profile" when empty). Editing shows Display name and Legal name side by side with "Legal name is optional; used only where a form asks for it…"; the legal name is shown as "Legal name: X ·".
- **Headline**, per profile, at most 160 characters on one line. When empty, a line "role · company · city, country" is built from the current or first role of the experience, or "Candidate profile".
- "Close" returns to the lists.

## Tabs

Files (with a badge counting suggestions to review), Profile, and Questions & Answers.

## Profile tab: Search setup

**What you're looking for:** Roles, Words, Except, Industries. Each is a tag input: Enter, comma, Tab or Add adds a tag; Backspace removes the last one; a pasted list is split; a duplicate ignoring case is skipped. How these decide which vacancies the profile sees is in `inbox.md`. A change applies at once; no vacancy is deleted by it.

**Compensation:** minimum and desired amount; currency (EUR or USD); before or after taxes; period (month or year); payment method (fiat only, crypto accepted, crypto only; crypto accepted by default); interest in equity or tokens in addition to base pay; day rate for contracts (minimum, target, currency). Amounts are above zero, the desired amount is not below the minimum, and a currency is required with any amount.

**Engagement, hours and work modes:** engagement (employment or contract); working time (full-time or part-time); for part-time, desired hours per week and optional minimum and maximum (maximum not below minimum); work modes (remote, hybrid, onsite). Hours apply only to part-time.

**Geography and authorization:**

- Current location (My data): a country and a city, chosen from suggestions.
- Preferred work locations (profile): regions, and specific countries and cities.
- Work authorization (My data): by country or region, offered with their size ("EU — European Union (27)"), each eligible or needing a visa.
- Willing to relocate: not set, yes or no.
- Desired time zones: from six fixed UTC bands.

**Availability** (My data): notice as a number and a unit (day, week, month) and an available-from date.

**Other requirements:** plain text with Markdown, read by the Shortlist check and by application preparation, not by Fits you ("Used by the Shortlist check and preparation; Fits you does not interpret this text").

**Inheritance.** An empty compensation, engagement, hours, work modes, preferred locations, time zones or industries field of a profile other than the first is shown dimmed with "from <first profile>" and is taken from the first profile when assessing. Editing it stores the profile's own value; saving an inherited value unchanged keeps it inherited. A change in the first profile recomputes the profiles that inherit it.

## Profile tab: Personal details

**Skills.** Chips coloured by level with a legend (Beginner, Proficient, Expert); the tooltip shows the level and where the skill came from. Editing shows rows of name and level, "+ Add skill" and ×. "Copy all skills to search" adds the skill names to the active profile's Words, skipping duplicates. Errors: "Skill N: enter a name", ""X" is both in your skills and among the skills you do not have", and a duplicate name.

**Skills you do not have**: chips with ×. **Confirmed by you**: "You have: …" and "You do not have: …" chips marked ✓ or ✕, with ×. Both are also written from the vacancy page (see `assessment.md`): + on a requirement offers a choice when the requirement names alternatives ("Go or Rust", "A/B"), adds known skills at level Proficient, splitting a list, and otherwise adds "You have: …"; − adds skills the user does not have or "You do not have: …".

**Experience:** company, role, start and end month and year, "Currently working here" (which disables the end), a Markdown summary, "Remove this role" and "+ Add role". Read mode shows "title · company" and dates ending in "– Present".

**Languages:** name and level (Pre-A1, A1 to C2, Native).

**Application form answers:** gender (with self-description), race (several values; "prefer not to say" excludes the others; with self-description), ethnicity, disability, veteran status, criminal record. They are used only to fill application forms. Every option shows its label.

## Files tab

**Files** ("CVs and notes. Nothing is analyzed until you choose Analyze."):

- "+ Add files" accepts several pdf, docx, md or txt files of at most 2 MiB each, also by drag and drop. "Paste text instead" takes a file name and text.
- A document card shows its name, its state, "kind · N characters" or a summary of its facts, an error with its cause and what to do ("Open Settings" when no provider is configured), and the actions Analyze, Cancel analysis, Retry analysis, View or Hide text, and Original.
- "Use as evidence" marks a document whose text the Shortlist check and application preparation may read.
- Deleting asks "Suggestions from it are removed; facts you already accepted stay in your profile."
- "N of M uploads failed. Successful files are kept."

**Contacts and links:** email, phone, LinkedIn, GitHub, X, website. "Fetched pages" are documents made from links ("+ Add link"), with delete, Retry analysis and "Refresh source". Errors: "Enter a URL.", a private address, a page behind a login, a page that is not HTML, a page without text. Adding the same address again returns the existing document. Adding or refreshing a link makes no model call; refreshing an unchanged page does nothing.

**Suggestions** ("✦ AI N to review" or "Nothing to review"):

- A card shows the field, the proposed value, the quote and the document ("(file deleted)" when gone), with Accept and Reject.
- A conflict shows Current against Proposed, with "Keep current" and "Use proposed".
- An invalid compensation offers "Edit compensation".
- Accepted and rejected cards fade out. A value the profile already has is not proposed.
- Personal facts (skills, experience, languages) go to My data; target facts (roles, compensation) go to the active profile.
- Accept answers at once; the recomputation runs in the background.

## Questions & Answers tab

A list of questions with their answers, shared by all profiles: Edit, Delete and "Add an answer". Application preparation writes to the same list (see `application.md`).

## Model calls

- **Analyze or Retry** on a document is the only model call of this area. It uses the provider chosen for reading in Settings, one call per chunk of the document, at most 100 facts, with a timeout of 120 seconds. A fact's quote must match the document text exactly. Headline and application form answers are never proposed.
- Saving any field recomputes Fits you of every vacancy for the affected profiles in the background, by code (see `assessment.md`).
- What a model receives in the Shortlist check and in application preparation: the accepted data of My data and the profile, and the text of documents marked "Use as evidence". Contacts, legal name and application form answers are never sent to a model.

## Not carried over

- The "Quick ranking up to date" block, the "Refresh scoring" button and the "Candidate context is not current" notice.
- The Settings › Candidate context screen and stored snapshots of candidate context.
- The candidate configuration file: its review notes and source roles are dropped, and its personal site, LinkedIn and GitHub become link documents.
- Archiving a profile; deleting with confirmation replaces it.
- A stored profile colour that the interface never read.
- Rich text in experience and other requirements; Markdown replaces it.
- The legacy skill levels and legacy text fields (location, eligibility, relocation, dealbreakers, work-mode limits) kept "until replaced"; they are converted once.
- The "Candidate facts and corrections" field on the vacancy page and its "this role only" scope; Confirmed by you replaces it.
- The desired overlap hours field, which had no input and no reader.
- The hint "Skill names are quick-ranking search signals", which was not true.
- The revision history of My data and profiles, which nothing read.

## Data from the old application

My data, every profile with its id, the active profile, the photo, documents with their files, suggestions still to review and in conflict, and the Questions & Answers list are imported. Fields are split between My data and profiles as above. Legacy skill levels become Production → Expert, Familiar → Proficient, Learning → Beginner. A legacy text field is dropped when the structured value exists and otherwise is appended to Other requirements. Rich text becomes Markdown. Old facts and corrections written on vacancies become "You have: …" or "You do not have: …" entries, or free text, in Confirmed by you, without scope. The personal site, LinkedIn and GitHub of the old candidate configuration become link documents. Revision histories, context snapshots and accepted or rejected suggestions are not imported; the source of an accepted skill is kept as a line. Before switch-over, each profile's visible vacancies match the old ones, Fits you is compared on a sample, and the import report lists every folded legacy value.
