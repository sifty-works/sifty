# Architecture

How Sifty is built. What the user sees is in `docs/behaviour/`; this document says how the code, the data and the background work are arranged to deliver it, and what limits the design must stay within.

## Design rules

These rules come from what made the previous application grow to several times the size its behaviour needed.

1. **One representation per fact.** A vacancy's text is stored once, as sanitised HTML. Plain text, sections, facts derived by code and the score breakdown are computed when read, not stored next to their source. A derived value is stored only when the list must filter or sort on it in SQL, and the table says so.
2. **Change is a version, not a hash in every table.** A vacancy has a `version` that rises when its normalised text changes. Everything derived from the text is keyed by `(vacancy_id, version)`, so no other table hashes the text and no stage has to freeze another to keep its cache valid.
3. **One mechanism per job.** One way to the job sites (`Fetcher`), one background runner, one model gate, one list of exclusions, one stage per vacancy and profile, one settings file.
4. **The server decides, the client shows.** Verdicts, thresholds, available actions and texts of states are computed on the server; the client never overrides them.
5. **A table needs a reader and an owner.** A table is added only when some code reads it and one capability owns it. Deleting a vacancy cascades to everything that belongs to it.
6. **Start does no work over the catalogue.** Anything that can be derived is derived when read, or queued as low-priority background work.
7. **A new feature names what it replaces.** A layer is not added next to the one it supersedes; the older one is removed in the same change.

## Process and layers

- One process, written in Go, with SQLite (pure Go driver) and a TypeScript frontend embedded in the binary. A second server on the same data folder is refused by a lock file `<data>/sifty.lock`.
- The server listens on `127.0.0.1` only, checks Host and Origin, requires JSON for every request that changes something, and limits a body to 1 MB except document uploads. Requests of 500 ms or more, other than model calls, are logged.
- Each capability has three layers: **handler** (parses and checks input, calls the service, writes JSON) → **service** (domain rules) → **repo** (SQL only, typed rows and typed errors). A foundation package `db` opens the database, runs migrations and makes backups; no other package reaches the raw connection.
- **Errors are typed:** `Invalid` → 422, `NotFound` → 404, `Conflict` → 409, `Busy` → 423, anything else and panics → 500 with "Internal error, see log #id" and a log entry. A Go error text never reaches the user. The store returns codes; texts for the user live in the service or the client.

## Capabilities and packages

| Capability | Packages | Behaviour |
|---|---|---|
| Collection | `fetch`, `source`, `listing`, `catalog`, `collect`, `industry` | `behaviour/collection.md` |
| Assessment | `reading`, `places`, `skills`, `fit`, `shortlistcheck` | `behaviour/assessment.md` |
| Inbox and vacancy page | `inbox` (list, decisions, deletion, exclusions, vacancy view) | `behaviour/inbox.md` |
| Profiles | `candidate` (My data, profiles, documents, proposals, Questions & Answers) | `behaviour/profiles.md` |
| Application | `locate`, `websearch`, `formread`, `answer` | `behaviour/application.md` |
| Foundation | `apperr`, `db`, `config`, `runner`, `models`, `server`, `cmd` | `behaviour/settings-and-data.md` |

### Collection

```go
type Fetcher interface { Get(ctx context.Context, url string, opt Opt) (Response, error) } // errors: ErrBlocked, ErrNotFound, ErrTransient
type Source interface {
    Match(u *url.URL) bool
    List(ctx context.Context, f Fetcher, s SiteConfig) ([]Posting, error)
}
type Detailer interface { Detail(ctx context.Context, f Fetcher, p Posting) (Posting, error) } // only where the listing has no text
func Normalize(raw string, kind Kind, vocab Vocab) string       // the only path from any input to stored HTML
func (c *Catalog) Upsert(p Posting, html, src string) (int64, Outcome) // New|Updated|Unchanged|Merged|Tombstoned|Excluded, one transaction
```

- `Fetcher` is the only way to job sites, vacancy pages, employers' career pages and link documents: public-address checks on every redirect, size limit, timeout, per-domain spacing and back-off, and a memory of blocked domains (`domain_block`) that survives a restart. The form-reading browser uses the same per-domain spacing and `domain_block`. Model adapters and the web search client have their own clients.
- Site quirks are data in `SiteConfig` (content selector, link pattern, feed address, skip JSON-LD), so a site-specific source is usually a configuration line over the generic source. The applicant-tracking-system client is shared by collection and by the employer search.
- `Detail` is called only when the listing has no text and the posting is new, or known without text and not requested in the last 7 days, and never while its domain is blocked; `Catalog.Known` tells which. The audit proposed requesting every known vacancy every 7 days to detect changes; that is not done, because a vacancy with text is never requested again.
- `Posting` carries the structured fields a source states (salary, employment type and the like) as a compact `source_fields` value, which the reader uses before asking a model.
- Domains are collected in parallel and sequentially within a domain. Each source's result is written to `source_run`.
- **Industries** (`industry`): a vocabulary of industries with keywords, shipped as embedded data and edited in Settings (written to `<data>/vocab/industries.yaml`). Industries are assigned by keyword match when a vacancy's title, tags or text change, and stored on the vacancy because the list filters on them in SQL. A change of the vocabulary queues reassignment of all vacancies as CPU work; a vacancy without text keeps its industries. No model call.

### Assessment

```go
type Reader interface { Read(ctx context.Context, in VacancyInput) (VacancyReading, error) } // one model call per text
func Score(p ProfileView, v VacancyReading, r Rules) Fit                                      // pure, no I/O
type ShortlistChecker interface { Check(ctx context.Context, f ProfileFacts, evidence string, v VacancyReading) (Check, error) }
type RequirementChecker interface { /* reserved; connected only after it beats code on labelled examples */ }
```

- `VacancyReading` holds conditions (money, work modes, engagement, hours, locations, time zone) and requirements (text, level, alternatives, quote as offsets). Code reads `source_fields` and labels first; the model is asked only for the rest, and every value must cite an exact excerpt. A reading is keyed by vacancy, version and `reader_version`; a new `reader_version` queues rereading at low priority.
- `Score` computes Fits you, the requirements counter, the four tiles and the requirement lines in one place. It takes about 0.01 ms per vacancy, so the breakdown is computed when a vacancy is opened. The list stores what it filters and sorts on: Fits you, the requirements score, the counts ✓, ✕ and ?, and the reading state.
- `ShortlistChecker` gets `EvidenceText` (see Profiles); the digest of its inputs covers the vacancy version, the projections it used and the evidence text. A changed digest marks the check outdated; it runs again when the vacancy page is opened.
- `places` is the one resolver of countries, regions, remote restrictions, onsite country and time-zone bands. There is no city table; cities are free text.
- `skills` is the one implementation of the skills vocabulary and of splitting requirement text into skills.

### Inbox and vacancy page

- One SQL query builds a list. For Inbox: the profile's visibility (target roles and keywords through the full-text index, "All of these", target industries) over vacancies with no decision in the profile. For Shortlist, Applied and Dismissed: the vacancies with that decision. Exclusions apply to all four. The query joins applications and the `fit` row and sorts on the server; filters (Fit, Collected, Industries, Sources, Unread) are applied in the same query, and their counts are one `GROUP BY` each over the same set.
- **Search** keeps substring semantics over title, company, tags and text. Title, company and tags are matched in SQL. The text is matched through the trigram full-text index `vacancy_fts`, with the query as one phrase; a query shorter than three characters matches title, company and tags only. Decompressing and scanning the bodies in Go was measured at 235 ms for 2,958 vacancies and replaced; the index answers in under 6 ms.
- The vacancy view is assembled in one package from the vacancy, its reading and `Score`; it says which actions are available and why.

### Profiles

```go
Effective(p, base Profile) Profile          // inheritance from the first profile, at read time
Goals(p) GoalsView; Evidence(me) []Fact      // inputs of Score, each with a Hash()
EvidenceText(me, p, docs) string             // Shortlist check and preparation; never contacts, legal name or form answers
Visibility(p) Filter
FormAnswers(me) map[string]string            // contacts, legal name, form answers: application only
```

- Saving writes one JSON record and emits `MeChanged` or `ProfileChanged(id)`. Consumers compare the hash of the projection they used, so only what changed is recomputed: a new keyword changes visibility only, a new skill recomputes Fits you of every profile, a new compensation of one profile recomputes only that profile. When visibility grows, vacancies that became visible without a reading are queued for reading.

### Application

```go
type Locator    interface { Locate(ctx context.Context, v Vacancy, deep bool) (Link, error) } // boards, then web search; no model
type WebSearch  interface { Search(ctx context.Context, q string) ([]Result, error) }       // Tavily, Brave, DuckDuckGo, or none
type FormReader interface { Read(ctx context.Context, url string) (Form, error) }            // browser; PasteReader has the same shape
type Answerer   interface { Answer(ctx context.Context, v Vacancy, f Form, have Answers) (Answers, error) } // Profile → Classifier → Drafter
```

- Entering Shortlist queues `locate`. When `locate` ends, it queues `prepare`, which reads the form from the employer's page or the original listing, then answers. A page found later by a manual `locate` queues `prepare` again. `locate` and `prepare` are deduplicated per vacancy, so no two searches of one vacancy run at once.
- `ProfileAnswerer` is a pure function; its answers are stored with `source=profile`. `Classifier` makes one batched call for unknown labels and caches them. `Drafter` selects evidence once and drafts all model fields in one call, with the shared context first so a prompt prefix cache applies.
- An edit by the user updates one field row with a revision check.

## Background work

```go
type Spec struct { Kind, Subject string; Priority int; Params json.RawMessage }
type Runner interface {
    Enqueue(Spec) (int64, error)          // deduplicated by (kind, subject) among queued and running
    Bump(kind, subject string, prio int)
    Cancel(id int64) error
    Subscribe() <-chan Event              // state, progress, current item
}
```

- One table `job(id, kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)` runs every kind of background work: `collect`, `read_vacancy`, `recompute_fit`, `assign_industries`, `shortlist_check`, `locate`, `prepare`, `analyze_document`, `retention`. Finished jobs are kept for 90 days.
- **Lanes are taken per stage, not per job:** `browser` (1) for reading a form, `net` (several) for collection and search, `cpu` (1) for recomputation. A model is not a lane; a stage that needs one takes the model gate per call and holds no lane while it waits, so reading a form never waits for a model.
- **Restart:** a job that was running becomes `interrupted`. Automatic kinds (`read_vacancy`, `recompute_fit`, `assign_industries`, `shortlist_check`, `retention`) are queued again while they have had fewer than three attempts, else `failed`. `locate` is queued again as a boards-only search. `collect`, `prepare` and `analyze_document` stay `interrupted` until the user retries; for `prepare` this follows the decision that failed preparations are not retried automatically.
- **Retries:** one policy, exponential pause, at most three attempts.
- **Schedule:** a ticker every five minutes queues `collect` when automatic collection is on and the interval since the start of the last run has passed. Loading the application calls `POST /api/collect` without `force`, which applies the same rule at once. "Collect jobs" calls it with `force`. `retention` is queued daily.
- **Status:** one endpoint or event stream replaces separate polls of queues, analysis and health. The list reloads only when decisions or visibility change; a new fit result reloads only visible rows or the open vacancy.

## Models

```go
type Model interface { Complete(ctx context.Context, r Request) (Response, error) }
type Request  struct { Prompt string; Schema json.RawMessage; Temperature *float64; Think string }
type Response struct { Text string; InTokens, OutTokens int }
```

- **Adapters:** `ollama` (with its own loader per instance), `openai` (any OpenAI-compatible endpoint, local or remote with an API key), `cli-claude`, `cli-opencode`. A registry maps a type to a constructor; a new provider is one file and one line. Command-line adapters run with a reduced environment and tools turned off, and reject an answer that tried to call a tool.
- **Kinds of work:** `reading` (vacancy texts, bulk; also profile documents), `shortlist_check`, `preparation`. Each has its own provider in settings.
- **Every call goes through `models.For(kind, class)`**, which wraps the model in three layers:
  1. **dispatcher**: user actions first, background calls pre-empted through `ctx`; one call at a time per model; every adapter honours `ctx`. A background call waits, with `not_before` on its job, while background model work is paused, while the day's count in `model_call` has reached the daily limit, and while the provider does not answer its check;
  2. **cost barrier**: each provider has `cost: local | free | paid`; bulk work on `paid` is refused unless the user allowed paid bulk work;
  3. **journal** `model_call`: time, kind, provider, model, characters and tokens in and out, duration, outcome, vacancy id, kept for 90 days. It is how the budgets below are measured.
- There is no lock on a model shared between processes; one process uses one model.

## Data

| Table | Holds |
|---|---|
| `vacancy` | id, identity, title, company, addresses, location, tags, `source_fields`, dates, first and last seen, `seen_at`, apply address and employer-search state, `content_hash`, `version`, text state, industries (stored for SQL filtering), `body` (sanitised HTML, compressed) |
| `vacancy_fts` | contentless full-text index (`content=''`, `contentless_delete=1`, `detail=full` for phrase queries), fed from the plain text at write |
| `vacancy_source` | where and under which address a vacancy was seen |
| `vacancy_alias` | old id → id after a merge |
| `tombstone` | canonical address of a removed vacancy, or a legacy hash |
| `exclusion` | scope (all profiles or one), kind (word or company), value |
| `domain_block` | blocked domain and until when |
| `source_run` | per run and source (keyed by the source's normalised address): status, counts found, new, updated, unchanged, excluded, removed, and error |
| `vacancy_reading` | the model's reading of a vacancy version with its `reader_version`: conditions and requirements, quotes as offsets |
| `fit` | profile, vacancy, Fits you, requirements score, counts ✓ ✕ ?, reading state, inputs version |
| `shortlist_check` | profile, vacancy, digest of inputs, result, date |
| `decision` | profile, vacancy, stage, reason, date; no row means Inbox |
| `application` | vacancy, date applied, profile and its name at the time |
| `me`, `profile` | My data and each profile as JSON |
| `document`, `proposal`, `qa` | profile documents (files under `<data>/files/`), proposed facts, Questions & Answers |
| `application_form`, `application_field`, `learned_phrasing` | last reading of a form, its fields with values and their source, learned labels |
| `job` | background work |
| `model_call` | journal of model calls |
| `settings` | what the application remembers about its use: active profile, pause of background model work |

**Size of a vacancy** (measured on the imported catalogue, then estimated): row with body 3.1 KB, sources 0.4 KB, full-text index 11 KB (trigram with positions, needed for phrase matching; without positions the candidates must be checked in Go, which costs 100 to 200 ms for common words), reading 0.8 to 1.2 KB, fit and decisions 0.5 KB: about 17.5 KB.

**Text retention.** A daily `retention` job applies the rule in `behaviour/settings-and-data.md`: it drops `body`, the index entry and the reading of the vacancy; about 0.5 KB of row, sources and decisions stays, with its industries. A row without `body` always takes the text when a source shows it again, whatever its `content_hash`. With about 55,000 new vacancies a year, growth falls from about 1 GB to about 30 MB a year for retained rows, plus the active window of about 160 MB.

**Migrations** are numbered SQL files embedded in the binary. At open: read `user_version`; refuse a newer one with a clear message; when migrations are pending, copy the database with `VACUUM INTO` into `<data>/backups/` (the last five are kept); run each migration with its `user_version` in one transaction.

## Configuration

- One file, `<data>/sifty.yaml`, holds what the user configures: sources (keyed by their normalised address), the collection interval and switch, providers and their cost, the provider of each kind of work, paid bulk permission, the daily model limit, the web search provider, weights and rates. Every key is optional. Defaults are one Go function; `sifty config defaults` prints the full file, so no template is stored and no personal value is in the repository.
- Vocabularies (regions, skills, sections, time zones, form phrasings, industries) are embedded data of their package, overridden by `<data>/vocab/<name>.yaml`.
- What the application remembers about its use lives in the `settings` table, not in the file.
- Secrets come only from the environment or `<data>/secrets.env`; saving a plain-text key in the file is refused.
- One `Validate`. Saving is parse → validate → backup (the last ten are kept, enough to undo a day of edits without unbounded growth) → atomic write → swap in memory; a form edits the YAML node so comments stay; when a step fails nothing is written.
- `--debug` or `debug: true` turns on diagnostic panels.

## Build and commands

- `make build`: frontend build, then `go build` with the frontend embedded; one binary. The browser script for form reading is embedded and written to the user's cache on first use; form reading needs Node.js and Playwright on the machine. DOCX text is read in Go.
- `make dev`: the frontend dev server proxying to `sifty serve --offline` on a copy of the data. `--offline` forbids every network request and model call. `SIFTY_REPLAY_DIR=<folder>` adds `--replay <folder>`, which serves requests from recorded responses in that folder instead of making real HTTP calls; unrecorded addresses are logged. Until switch-over, the copy is a fresh `import-v1` of a copy of the old database.
- `make check`, `make budget`, `make release`, `make rollback`: see `PROCESS.md`.
- Commands: `serve` (`--offline`, `--replay <folder>`, `--port`, `--debug`, `--data-dir`), `backup` (database copy and the user's files in one archive), `import-v1`, `config defaults`, `version`.

## Frontend

- `api/client.ts`: one typed function per endpoint. `state/`: the list cache by profile, stage and query, optimistic decisions, decisions of the session, filters, query and sort remembered in the browser. `views/`: shell, tabs, list, row, filters, vacancy view, fit band, ask and offer cards, listing, Shortlist check, dialogs, profile pages, settings pages. `keyboard.ts`: keys by physical position.
- Views do not call the API directly and compute no verdict or threshold.

## Endpoints

| Group | Endpoints |
|---|---|
| Lists and vacancies | `GET /api/vacancies?profile&stage&q&sort&filters` (rows of about 350 bytes, counts per stage and per filter value); `GET /api/vacancies/{id}?profile`; `POST /api/vacancies/{id}/decision {from,to,reason?,confirmed?}`; `POST /api/vacancies/{id}/seen`; `DELETE /api/vacancies/{id}`; `POST /api/vacancies/{id}/refresh` |
| Exclusions and industries | `GET/POST/DELETE /api/exclusions`, `GET /api/exclusions/preview`; `GET/PUT /api/industries` |
| Collection | `GET/POST/PATCH/DELETE /api/sources`; `PUT /api/sources/schedule`; `POST /api/collect {force}` |
| Profiles | `GET/PATCH /api/me`, `PUT/DELETE /api/me/photo`; `GET/POST/PATCH/DELETE /api/profiles`, `PUT /api/active-profile`; `POST /api/me/skills` (+ and − from a vacancy) |
| Documents | `POST/DELETE /api/documents`, `POST /api/documents/{id}/analyze`, `POST /api/documents/{id}/refresh`; `POST /api/proposals/{id}/accept`, `/reject` |
| Questions & Answers | `GET/POST/PATCH/DELETE /api/qa` |
| Application | `GET /api/vacancies/{id}/application`; `PATCH …/application/fields/{field}`; `POST …/application/{write,paste,questions,retry}`; `POST /api/vacancies/{id}/locate`; `POST /api/vacancies/{id}/shortlist-check/retry` |
| Settings and status | `GET/PUT /api/settings`, `GET/PUT /api/settings/file`; `POST /api/providers/{name}/check`; `GET /api/jobs`; `GET /api/status` (or event stream) |

## Import of the old application

`sifty import-v1` reads a copy of the old database opened read-only, accepts only the known schema versions, writes into an empty data folder, and creates no HTTP client and no model. What is carried over and dropped is in `behaviour/settings-and-data.md` and in each behaviour document. Technical points:

- Vacancy ids are kept; the next id is above every id and every alias. Every vacancy starts at version 1.
- Old removal marks are SHA-256 hashes of the old canonical address. The old canonicalisation is kept as one function, `legacyKey`, and the catalogue checks both it and the new canonical address.
- Stored model readings are attached to version 1 of their vacancy, not matched by text hash, because the new normaliser changes the plain text slightly; their quotes are located again as offsets, and a quote that is not found is kept as text.
- The comparison before switch-over follows `behaviour/settings-and-data.md`.
- `import-v1` is one-time code: it lives in its own package, is removed after switch-over, and is not counted in the budgets.

## Budgets

These are targets the coordinator checks at acceptance of every change (see `PROCESS.md`). Going over one needs the owner's explicit decision.

| Measure | Budget |
|---|---|
| Backend code | at most 16,000 lines |
| Frontend code | at most 6,000 lines |
| Tests | no more lines than the code they test |
| Database per vacancy | at most 18 KB |
| Model calls for a new vacancy text | 1 |
| Model calls for a change of profile or My data | 0 for Fits you and the counter; Shortlist checks become outdated and run again only when their vacancy is opened |
| Shortlist check | at most 1 per shortlisted vacancy on entering Shortlist, and 1 per opening after its inputs changed |
| Preparation of one form | 0 to 1 to classify labels, 1 to 2 to draft (one retry of an empty reply); Write and Paste 1 each |
| Network per collection run | 1 request per source, plus 1 per vacancy without text in the listing that is new, or known without text and not requested in the last 7 days |
| List API | under 50 ms at the 95th percentile for 1,200 rows; vacancy view under 30 ms; decision under 20 ms |

Estimated size by capability (backend, then frontend, in lines): collection 3,000 and 250; assessment 3,100 and 700; inbox 1,200 and 1,500; profiles 2,700 and 2,500; application 2,800 and 800; foundation 2,700 and 700 (settings pages). That is about 15,500 and 6,450. The frontend estimate is about 450 lines over its budget; the profile editors are where it must be recovered, with one generic section editor instead of one editor per section.
