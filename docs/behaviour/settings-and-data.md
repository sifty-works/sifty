# Settings, data and models

What the user gets: the application starts on an empty data folder with no setup, keeps everything in that one folder on the user's machine, sends data only to job sites, web search and the model providers the user configured, and lets the user choose a provider for each kind of model work. Nothing paid is used in bulk without the user's explicit permission.

## Where data lives

- All data lives in one folder, chosen by `--data-dir`, then the `SIFTY_DATA_DIR` environment variable, then `./data`. The path is made absolute. The folder is created with access for its owner only. Nothing is written outside it.
- The server listens on `127.0.0.1` only. It checks the Host and Origin of every request, requires JSON for every request that changes something, and limits a request body to 1 MB, except uploads of profile documents.
- **What leaves the machine:** requests to job sites, requests to web search providers, and prompts to the model providers the user configured. There is no telemetry.

## First start

The application starts on an empty data folder without copying any template. All settings live in one file in the data folder, and every key in it is optional; a missing key takes its default. The first run shows empty lists and a hint to add sources.

## Settings

- **Sources:** the sources, the automatic collection switch and the interval (see `collection.md`).
- **Exclusions:** the one list of exclusions (see `inbox.md`).
- **Industries:** the vocabulary of industries and their keywords (see `collection.md`).
- **Models:**
  - a provider for each kind of model work: **Reading** (requirements of vacancies and facts of profile documents), **Shortlist check**, and **Preparation** (form labels, drafts, cover letters, pasted forms);
  - the state of each provider and a "Check" button that sends a fixed test prompt with no user data;
  - "Allow paid bulk work", off by default;
  - a switch that pauses background model work, and a daily limit of background model calls.
- **Background work:** what is running, what is waiting and what failed, with the reason (see `application.md`).
- **Advanced:** the settings file in a text editor, next to the forms. Saving checks the file first and keeps a backup; the last ten backups are kept, and the editor names the folder where they are.

A change applies at once without a restart, except a change of the port. A change that affects results (weights, vocabularies) starts a background recomputation and the answer returns at once.

## Model providers

- The first version supports four kinds of provider: **Ollama**, **OpenAI-compatible** (local or remote, with an API key), **Claude CLI** and **opencode CLI**.
- Each kind of model work uses its own provider. There is no silent fallback to another provider, and no fallback to a paid one.
- Data leaves the machine only to the providers the user configured.
- Bulk work (reading vacancy texts) refuses a provider that is neither local nor marked free, unless "Allow paid bulk work" is on.
- A user's action goes ahead of background model work. One model serves one call at a time.
- **API keys** come only from the environment or from a secrets file in the data folder, read at start without overriding variables already set. Saving settings that contain a key in plain text is refused.
- **Command-line providers** run with a reduced environment and with tools, extensions and project settings turned off. An answer in which the model tried to call a tool is rejected.
- **Ollama:** the model is loaded once before the first call and kept loaded for an hour by default; loading may take up to 600 seconds and shows "Loading model <name> · m:ss"; the context size follows the length of the prompt, and a prompt that would be cut is refused; loaded models are unloaded when the application stops.
- A prompt longer than the provider's context gives an explicit error, never a silently shortened prompt.

## Keeping data

- Vacancies are never deleted automatically. Dismissed vacancies stay, so that collection does not bring them back as new. A vacancy is removed only by "Delete role…" (see `inbox.md`), which leaves a mark of its addresses so collection keeps it away.
- **Text retention.** The text of a vacancy that is dismissed or was never opened, and that its source has not shown for 60 days, is dropped together with its full-text index entry. Its row stays: title, company, addresses, dates and decision. Search no longer finds it by its text. When a source shows it again, its text is fetched again and its decision stays. Vacancies in Shortlist or Applied keep their text.
- A full-text index of vacancy texts is kept for search.
- Of My data and of each profile only the latest version is kept; there is no history of edits.
- The application is for one user; it keeps no user accounts.
- Changes of the database layout are applied automatically at start, inside one transaction, after the application has copied the database next to it. An older version of the application refuses to open a database of a newer layout.

## Errors

- An error is shown to the user in words, with what to do when there is something to do. An internal failure shows a short message and is written to the log with its details; it never shows a stack or an internal value.
- An unknown address of the server's interface answers "Refresh the page…".
- A request that takes 500 ms or more, other than a model call, is written to the log.

## Not carried over

- Copying templates by hand before the first start, and the four configuration files that had to exist.
- The candidate configuration file and the Settings › Candidate context screen (see `profiles.md`).
- The assessment queue page with its switch and its table (see `assessment.md`); the Background work page replaces it.
- The "Extraction" stage label that promised a fallback no code used, the hidden matching stage, and runtime keys that were read and never applied.
- The order of background work following the sort.
- Prompt copies in the configuration folder that nothing read.
- Loopback-only HTTP providers; remote providers with an API key are allowed.
- A lock on the local model shared between several application processes on one machine; one process uses one model.
- Revision histories of My data, profiles and drafts.
- Unlimited configuration backups.
- Raw error values and panic texts shown in the interface.

## Data from the old application

The import reads a copy of the old database, opened read-only, and writes into an empty data folder; it refuses an unknown database version and makes no network request and no model call. It carries over everything the user made or paid for: vacancies with their ids, sightings, aliases, removal marks, exclusions, profiles, documents and their files, the photo, Questions & Answers, learned label phrasings, decisions, applications, drafts, requirements already read by a model, and the secrets file. The old configuration files become one settings file holding only the values that differ from the defaults, and the import report lists them. Full-text index, sections and Fits you are rebuilt. Page originals, snapshots, old candidate contexts, old detailed assessments, intermediate result tables, run journals (except the time of the last successful collection) and locks are dropped. The import report counts every kind of record before, after and dropped, with the reason, and gives a SHA-256 of every copied file.

The old application keeps running unchanged until switch-over. Until then the new one runs daily on a fresh copy, in its own data folder and on another port, only to compare: per profile the list ids, order and stages; twenty saved searches; Fits you on every vacancy, with the expected differences listed beforehand; and a hundred vacancy pages. Switch-over is: stop the old application, run the final import, check it, start the new one on the old port. The old data folder stays untouched for a rollback; there is no double writing.
