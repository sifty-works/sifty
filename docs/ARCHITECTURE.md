# Architecture

How Sifty is built. What the user sees is in `docs/behaviour/`; this document says how the code, the data and the background work are arranged to deliver it, and what limits the design must stay within.

## Design rules

These rules come from what made the previous application grow to several times the size its behaviour needed.

1. **One representation per fact.** A vacancy's text is stored once, as sanitised HTML. Plain text, sections, facts derived by code and the score breakdown are computed when read, not stored next to their source.
2. **Change is a version, not a hash in every table.** A vacancy has a `version` that rises when its normalised text changes. Everything derived from the text is keyed by `(vacancy_id, version)`, so no other table hashes the text and no stage has to freeze another to keep its cache valid.
3. **One mechanism per job.** One network exit (`Fetcher`), one background runner, one model gate, one list of exclusions, one stage per vacancy and profile, one settings file.
4. **The server decides, the client shows.** Verdicts, thresholds, available actions and texts of states are computed on the server; the client never overrides them.
5. **A table needs a reader and an owner.** A table is added only when some code reads it and one capability owns it. Deleting a vacancy cascades to everything that belongs to it.
6. **Start does no work over the catalogue.** Anything that can be derived is derived when read, or queued as low-priority background work.
7. **A new feature names what it replaces.** A layer is not added next to the one it supersedes; the older one is removed in the same change.

## Process and layers

- One process, written in Go, with SQLite (pure Go driver) and a TypeScript frontend embedded in the binary. A second server on the same data folder is refused by a lock file `<data>/sifty.lock`.
- Each capability has three layers: **handler** (parses and checks input, calls the service, writes JSON) → **service** (domain rules) → **repo** (SQL only, typed rows and typed errors). A foundation package `db` opens the database, runs migrations and makes backups; no other package reaches the raw connection.
- **Errors are typed:** `Invalid` → 422, `NotFound` → 404, `Conflict` → 409, `Busy` → 423, anything else and panics → 500 with "Internal error, see log #id" and a log entry. A Go error text never reaches the user. The store returns codes; texts for the user live in the service or the client.

## Capabilities and packages

| Capability | Packages | Behaviour |
|---|---|---|
| Collection | `fetch`, `source`, `listing`, `catalog`, `collect`, `industry` | `behaviour/collection.md` |
| Assessment | `reading`, `places`, `skills`, `fit`, `shortlistcheck` | `behaviour/assessment.md` |
| Inbox and vacancy page | `inbox` (list, decisions, deletion, exclusions, vacancy view) | `behaviour/inbox.md` |
| Profiles | `candidate` (My data, profiles, documents, proposals, Questions & Answers) | `behaviour/profiles.md` |
| Application | `locate`, `formread`, `answer` | `behaviour/application.md` |
| Foundation | `db`, `config`, `runner`, `models`, `server`, `cmd` | `behaviour/settings-and-data.md` |

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

- `Fetcher` is the only network exit of the application: public-address checks on every redirect, size limit, timeout, per-domain spacing and back-off, and a memory of blocked domains kept in the database so it survives a restart.
- Site quirks are data in `SiteConfig` (content selector, link pattern, feed address, skip JSON-LD), so a site-specific source is usually a configuration line over the generic source. The applicant-tracking-system client is shared by collection and by the employer search.
- `Detail` is called only for new postings and for known ones without text; `Catalog.Known` tells which.
- Domains are collected in parallel and sequentially within a domain. Each source's result is written per run.
- **Industries** (`industry`): a vocabulary of industries with keywords, shipped as embedded data and edited in Settings (written to `<data>/vocab/industries.yaml`). Industries of a vacancy are assigned by keyword match when the vacancy is saved with a new version; a change of the vocabulary queues reassignment of all vacancies as CPU work. No model call.

### Assessment

```go
type Reader interface { Read(ctx context.Context, in VacancyInput) (VacancyReading, error) } // one model call per text
func Score(p ProfileView, v VacancyReading, r Rules) Fit                                      // pure, no I/O
type ShortlistChecker interface { Check(ctx context.Context, f ProfileFacts, v VacancyReading) (Check, error) }
type RequirementChecker interface { /* reserved; connected only after it beats code on labelled examples */ }
```

- `VacancyReading` holds conditions (money, work modes, engagement, hours, locations, time zone) and requirements (text, kind, level, alternatives, quote as offsets). Structured source fields are read by code first; the model is asked only for the rest, and every value must cite an exact excerpt.
- `Score` computes Fits you, the requirements counter, the four tiles and the requirement lines in one place. It takes about 0.01 ms per vacancy, so the breakdown is computed when a vacancy is opened. Only the two numbers are stored, to let the list sort and filter in SQL.
- `places` is the one resolver of countries, regions, remote restrictions, onsite country and time-zone bands. There is no city table.
- `skills` is the one implementation of the skills vocabulary and of splitting requirement text into skills.

### Inbox and vacancy page

- One SQL query builds a list: the profile's visibility (target roles and keywords through the full-text index, target industries, exclusions) joined with decisions, applications and the two fit numbers, filtered by stage and query (`instr` keeps substring semantics), sorted on the server. Counters are one `GROUP BY stage` over the same set.
- The vacancy view is assembled in one package from the vacancy, its reading and `Score`; it says which actions are available and why.

### Profiles

```go
Effective(p, base Profile) Profile          // inheritance from the first profile, at read time
Goals(p) GoalsView; Evidence(me) []Fact      // inputs of Score, each with a Hash()
EvidenceText(me, docs) string                // prompts of the Shortlist check and preparation; never contacts or form answers
Visibility(p) Filter
FormAnswers(me) map[string]string            // contacts, legal name, form answers: application only
```

- Saving writes one JSON record and emits `MeChanged` or `ProfileChanged(id)`. Consumers compare the hash of the projection they used, so only what changed is recomputed: a new keyword changes visibility only, a new skill recomputes Fits you of every profile, a new compensation of one profile recomputes only that profile.

### Application

```go
type Locator    interface { Locate(ctx context.Context, v Vacancy, deep bool) (Link, error) } // boards, then web search; no model
type FormReader interface { Read(ctx context.Context, url string) (Form, error) }            // browser; PasteReader has the same shape
type Answerer   interface { Answer(ctx context.Context, v Vacancy, f Form, have Answers) (Answers, error) } // Profile → Classifier → Drafter
```

- Preparation is one job per vacancy that runs Locate → Read → Answer in order.
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

- One table `job(id, kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)` runs every kind of background work: `collect`, `read_vacancy`, `recompute_fit`, `assign_industries`, `shortlist_check`, `prepare`, `locate`, `analyze_document`, `retention`.
- **Lanes:** `browser` (1), `net` (several), `cpu` (1). A model is not a lane; it is a gate taken per call.
- **Restart:** a job that was running becomes `interrupted`. Automatic kinds (`read_vacancy`, `recompute_fit`, `assign_industries`, `retention`) are queued again while `attempts < 3`, else `failed`. Kinds the user sees as their own work (`collect`, `prepare`, `analyze_document`, `shortlist_check`, `locate`) stay `interrupted` until the user retries.
- **Retries:** one policy, exponential pause with a ceiling of attempts.
- **Schedule:** a ticker every five minutes queues `collect` when automatic collection is on and the interval since the last finished run has passed. "Collect jobs" queues it with `force`.
- **Status:** one endpoint or event stream replaces separate polls of queues, analysis and health. The list reloads only when decisions or visibility change; a new fit result reloads only visible rows or the open vacancy.

## Models

```go
type Model interface { Complete(ctx context.Context, r Request) (Response, error) }
type Request  struct { Prompt string; Schema json.RawMessage; Temperature *float64; Think string }
type Response struct { Text string; InTokens, OutTokens int }
```

- **Adapters:** `ollama` (with its own loader per instance), `openai` (any OpenAI-compatible endpoint, local or remote with an API key), `cli-claude`, `cli-opencode`. A registry maps a type to a constructor; a new provider is one file and one line.
- **Kinds of work:** `reading` (vacancy texts, bulk; also profile documents), `shortlist_check`, `preparation`. Each has its own provider in settings.
- **Every call goes through `models.For(kind, class)`**, which wraps the model in three layers:
  1. **dispatcher**: user actions first, background calls pre-empted through `ctx`; one call at a time per model; every adapter honours `ctx`;
  2. **cost barrier**: each provider has `cost: local | free | paid`; bulk work on `paid` is refused unless the user allowed paid bulk work;
  3. **journal** `model_call`: time, kind, provider, model, characters and tokens in and out, duration, outcome, vacancy id. It is how the budgets below are measured.
- There is no lock on a model shared between processes; one process uses one model.

## Data

| Table | Holds |
|---|---|
| `vacancy` | id, identity, title, company, addresses, location, tags, dates, first and last seen, `seen_at`, apply address and employer-search state, `content_hash`, `version`, text state, industries, `body` (sanitised HTML, compressed) |
| `vacancy_fts` | contentless full-text index (`content=''`, `contentless_delete=1`, `detail=full` for phrase queries), fed from the plain text at write |
| `vacancy_source` | where and under which address a vacancy was seen |
| `vacancy_alias` | old id → id after a merge |
| `tombstone` | canonical address of a removed vacancy, or a legacy hash |
| `exclusion` | scope (all profiles or one), kind (word or company), value |
| `domain_block` | blocked domain and until when |
| `source_run` | per run and source: status, counts found, new, updated, unchanged, excluded, removed, and error |
| `vacancy_reading` | the model's reading of a vacancy version: conditions and requirements, quotes as offsets |
| `fit` | profile, vacancy, Fits you, requirements score, inputs version |
| `shortlist_check` | profile, vacancy, digest of inputs, result |
| `decision` | profile, vacancy, stage, reason, date; no row means Inbox |
| `application` | vacancy, date applied, profile |
| `me`, `profile` | My data and each profile as JSON |
| `document`, `proposal`, `qa` | profile documents (files under `<data>/files/`), proposed facts, Questions & Answers |
| `application_form`, `application_field`, `learned_phrasing` | last reading of a form, its fields with values and their source, learned labels |
| `job` | background work |
| `model_call` | journal of model calls |
| `settings` | state: active profile, switches |

**Size of a vacancy** (estimate): row 0.5 KB, body 1.8 KB compressed, full-text index 7.1 KB, reading 0.8 to 1.2 KB, fit and decisions 0.5 KB: about 11 KB.

**Text retention.** A `retention` job drops `body` and the index entry of a vacancy that is dismissed or never opened and not seen at any source for 60 days; the row of about 0.4 KB stays. Growth falls from about 0.65 GB to about 30 MB a year.

**Migrations** are numbered SQL files embedded in the binary. At open: read `user_version`; refuse a newer one with a clear message; when migrations are pending, copy the database with `VACUUM INTO` into `<data>/backups/` (the last five are kept); run each migration with its `user_version` in one transaction.

## Configuration

- One file, `<data>/sifty.yaml`; every key is optional. Defaults are one Go function; `sifty config defaults` prints the full file, so no template is stored and no personal value is in the repository.
- Vocabularies (regions, skills, sections, time zones, form phrasings, industries) are embedded data of their package, overridden by `<data>/vocab/<name>.yaml`.
- State that the user switches in the interface lives in the `settings` table, not in the file.
- Secrets come only from the environment or `<data>/secrets.env`.
- One `Validate`. Saving is parse → validate → backup (last ten kept) → atomic write → swap in memory; when a step fails nothing is written.

## Build and commands

- `make build`: frontend build, then `go build` with the frontend embedded; one binary. The browser script for form reading is embedded and written to the cache on first use; form reading needs Node.js and Playwright on the machine. DOCX text is read in Go.
- `make dev`: the frontend dev server proxying to `sifty serve --offline` on a copy of the data. `--offline` forbids every network request and model call.
- Commands: `serve`, `backup` (database copy and the user's files in one archive), `import-v1`, `config defaults`, `version`.

## Frontend

- `api/client.ts`: one typed function per endpoint. `state/`: the list cache by profile, stage and query, optimistic decisions, decisions of the session, remembered filters. `views/`: shell, tabs, list, row, filters, vacancy view, fit band, ask and offer cards, listing, Shortlist check, dialogs, profile and settings pages. `keyboard.ts`: keys by physical position.
- Views do not call the API directly and compute no verdict or threshold.

## Main endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/vacancies?profile&stage&q&sort` | rows of about 350 bytes and counts per stage |
| `GET /api/vacancies/{id}?profile` | the vacancy view |
| `POST /api/vacancies/{id}/decision {from,to,reason?,confirmed?}` | a decision with an explicit expected stage |
| `POST /api/vacancies/{id}/seen` | read mark |
| `DELETE /api/vacancies/{id}` | removal, or 409 with a code |
| `GET/POST/DELETE /api/exclusions`, `GET /api/exclusions/preview` | exclusions |
| `GET /api/status` (or event stream) | background work and change counters |

## Import of the old application

`sifty import-v1` reads a copy of the old database opened read-only, accepts only the known schema versions, writes into an empty data folder, and creates no HTTP client and no model. What is carried over and dropped is in `behaviour/settings-and-data.md` and in each behaviour document. Technical points:

- Vacancy ids are kept; the next id is above every id and every alias.
- Old removal marks are SHA-256 hashes of the old canonical address. The old canonicalisation is kept as one function, `legacyKey`, and the catalogue checks both it and the new canonical address.
- A stored model reading is carried over when its text hash matches the imported text; its quotes are located again as offsets, and a quote that is not found is kept as text.
- The comparison before switch-over runs the old and the new application on copies with background work paused and compares the lists of every profile, twenty saved searches, Fits you of every vacancy against a list of expected differences, and a hundred vacancy views.

## Budgets

These are targets the coordinator checks at acceptance of every change (see `PROCESS.md`). Going over one needs the owner's explicit decision.

| Measure | Budget |
|---|---|
| Backend code | at most 16,000 lines |
| Frontend code | at most 6,000 lines |
| Tests | no more lines than the code they test |
| Database per vacancy | at most 14 KB |
| Model calls for a new vacancy text | 1 |
| Model calls for a change of profile or My data | 0 |
| Shortlist check | at most 1 per shortlisted vacancy and change of its inputs |
| Preparation of one form | 0 to 1 to classify labels, 1 to draft |
| Network per collection run | 1 request per source, plus 1 per new vacancy without text in the listing |
| List API | under 50 ms at the 95th percentile for 1,200 rows; vacancy view under 30 ms; decision under 20 ms |

Estimated size by capability (backend, then frontend, in lines): collection 3,000 and 250; assessment 3,100 and 700; inbox 1,200 and 1,500; profiles 2,700 and 2,500; application 2,800 and about 800; foundation 2,700. That is about 15,500 and 5,750.
