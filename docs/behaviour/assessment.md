# Assessment

What the user gets from assessment: every vacancy in their Inbox carries one number, Fits you, that says how well the vacancy fits their goals, and a counter of its requirements that says how well they fit the vacancy. Both are computed by code from the user's own data, change at once when the user corrects that data, and never hide a vacancy. A model is used only to read a vacancy's requirements once per text and for one check when a vacancy enters Shortlist.

## What the user states

**Goals, per profile:** target roles; minimum compensation with currency, period and tax basis; engagement; hours; work modes; preferred locations; time zones, chosen from six fixed UTC bands; dealbreakers. An empty goal of a role profile is taken from the first profile when assessing and is not copied into the role profile; changing it in the first profile recomputes the role profiles that inherit it (see `profiles.md`).

**My data, shared by all profiles:** skills with a level; skills the user does not have; "Confirmed by you", a list of "You have: …" and "You do not have: …" statements, which also holds free-text facts and corrections about the user; experience, from which code derives total years, years in leadership roles, years in engineering roles, founder experience and work in the last year; languages; current location; work authorizations by country or by region, where a region covers its countries (the EU covers its member states).

## Fits you

Fits you is a number from 0 to 100 and the only number the user sees. It is a weighted average of the criteria below; a criterion that cannot be compared counts as 0.5. A profile with no goals gives no number.

| Criterion | Starting weight | How it is compared |
|---|---|---|
| Role | 0.25 | best word overlap of the vacancy title with a target role |
| Money | 0.20 | `min(1, top of the vacancy range / user's minimum)` after conversion |
| Location | 0.075 | see Location below |
| Remote restriction | 0.075 | see Location below |
| Time zone | 0.10 | 1 when the vacancy and the user share a band |
| Work mode | 0.04 | remote, hybrid or onsite against the user's work modes |
| Engagement | 0.03 | employment, contract or freelance against the user's goal |
| Hours | 0.03 | full-time or part-time against the user's goal |
| Onsite work authorization | set from data | see Location below |
| Requirements | set from data | the requirements score below |

Weights live in configuration. The weights of onsite work authorization and of requirements are chosen by measurement on hand-labelled vacancies before the first release.

**Money.** The vacancy's range is converted to the user's currency by rates from configuration. A sum without a tax note is gross. A monthly sum is multiplied by 12, an hourly sum by 2080.

**Location.** Unrestricted remote ("Anywhere", "Remote", telecommute) matches both location criteria. Restricted remote ("US only", "EU") is checked against the user's work authorizations, not against where the user lives: covered, not covered or unknown. Onsite and hybrid vacancies are checked the same way, by the vacancy's country against the work authorizations; when the vacancy states that it sponsors a work visa, a missing authorization is not a mismatch. Composite labels are read with a vocabulary of countries and regions; a place the vocabulary does not know is unknown.

**Time zone.** The vacancy's bands come from a stated time zone, then from phrases in the text, then from the countries and regions of its locations.

**Industries** do not enter Fits you. They decide which vacancies a profile's search shows (see `inbox.md` and `profiles.md`).

**Buckets.** Strong is 75 and above, Partial is 55 to 74, Weak is below 55, and Not scored has no number. The Fit filter of the Inbox uses these buckets.

## Requirements

- A vacancy's requirements are read from its text once per text, each with its kind (required or nice to have), alternatives and the exact phrase it was read from.
- Each requirement is decided by code against My data: ✓ when the skill is in the user's skills by the skills vocabulary or the requirement is confirmed as had; ✕ when it is in the skills the user does not have or confirmed as not had; ? otherwise.
- The counter "✓ 4 · ✕ 1 · ? 6" is shown in the Inbox row and on the vacancy page.
- The requirements score is the average of ✓ = 1, ✕ = 0 and ? = 0.5, with required requirements counted twice and nice-to-have ones once. It enters Fits you as the Requirements criterion.
- Model matching of requirements is not part of the first version. A later checker can be connected through the same interface, and only after it decides hand-labelled examples better than code.

## Facts of a vacancy

Structured fields and labels of the source are read by code first. A model fills only what code did not find. Every value the model gives must cite an exact excerpt of the vacancy text, or it is dropped. Engagement, work mode and hours are accepted from the model only when the excerpt is a phrase of the right kind.

## In the Inbox

- The row shows Fits you as a filled mark and the requirements counter. The tooltip reads "Fits you 82: Strong · ✓ 4 · ✕ 1 · ? 6".
- Empty states of Fits you: "Not compared yet", "Full job description is unavailable", "Your profile states no goals to compare".
- Empty states of the counter: "Reading requirements…", "Requirements not read yet", "No requirements stated", "Not checked yet" (no requirement is decided), "Full job description is unavailable".
- A result that is being recomputed after a change is dimmed until the new one is ready.
- **Best fit** sorts by Fits you, highest first, then by the requirements score, then by id; dimmed results and vacancies without a number come after the current ones.
- **Newest** sorts by publication date in three ranks: a date with its basis, a date without one, no date.
- The chosen sort is remembered for the user.

## On the vacancy page

From top to bottom:

1. **Fits you band.** "Fits you 82 /100", the bucket and the requirements counter, and the "Refresh source" button (see `collection.md`). On the right, four tiles: Money, Location, Work mode, Contract. Each shows the vacancy's value, a line comparing it with the user's goal, and a verdict ✓, ✕ or ?. A tile's verdict is the verdict of the criteria behind it, so a tile never disagrees with the number. Money shows the original range and "≈ €…" after conversion.
2. **What they ask.** "N requirements" with the counter, then the groups Required and Nice to have. Each row shows the requirement, its alternatives, its verdict, and on the right the evidence from My data ("17 years", "Go · Rust").
   - **+** ("I have this") adds the skill to Skills at the level Proficient when the skills vocabulary knows it, asking which one when the requirement has alternatives; otherwise it adds "You have: …" to Confirmed by you.
   - **−** adds the skill to the skills the user does not have, or "You do not have: …" to Confirmed by you.
   - After a click this vacancy is recomputed at once, on the page and in its Inbox row; other vacancies follow in the background. A notice says what was added and where, for example "Added "Go" to your skills (level Proficient; change in your profile)."
3. **What they offer** and **Listing text**, described in `inbox.md`.
4. **Shortlist check**, only for vacancies in Shortlist (see below).

## Shortlist check

When a vacancy enters Shortlist, one model call compares the user's confirmed data with the vacancy's requirements. Its answer is shown on the vacancy page next to the requirements that code left as ?, each with a short reason and the excerpt it relies on. It does not change Fits you or the counter. The check runs again only when its inputs change: the vacancy text, My data or the profile's goals. There is no Assess or Reassess button. The check uses the model chosen in Settings; by default it must be a local model or one the user chose explicitly.

## When things are recomputed

| Change | What is recomputed | Model calls |
|---|---|---|
| New or changed vacancy text | its facts and requirements are read; its Fits you for every profile | 1 to read the text |
| A change of goals or My data | Fits you of every vacancy for the affected profiles, in the background | none |
| + or − on a requirement | this vacancy at once, the rest in the background | none |
| A change of weights or vocabularies | everything | none |
| A change of the reading prompt or schema | requirements of every vacancy are read again | 1 per text |
| A vacancy enters Shortlist | the Shortlist check | 1 |

Texts are read in the background, one at a time, in the order the vacancies arrived, and only for vacancies visible in at least one profile. A reading is retried up to three times. Bulk reading refuses a provider that is neither local nor free unless the user allowed paid bulk work in Settings.

## Not carried over

- A separate You fit number; the requirements counter and the Requirements criterion replace it.
- The detailed model assessment: its summary, evidence, unknown employer details, provenance, provider choice, queue status line, the Assess and Assess now buttons, and the queue switch in Settings. The Shortlist check replaces it.
- The model matching pass over all vacancies.
- The separate "Candidate facts and corrections" form with its scope; Confirmed by you holds these facts.
- A world city table for time zones; countries and regions are enough.
- Fits you shown twice on the page, and the note pointing to a tab where the assessment no longer is.
- Freshness flags of the profile and the "Refresh scoring" button.
- The background order following the sort switch.
- The "AI assessment" filter of the Inbox.

## Data from the old application

Requirements and facts already read by a model are imported for every vacancy whose text is unchanged, so most vacancies need no model call after the switch; their excerpts are located again in the normalised text, and an excerpt that is not found is kept as text. Skills, skills the user does not have and confirmed statements are imported as they are; the old free-text facts and corrections are imported into Confirmed by you. Fits you and the requirements counter are recomputed by code for every vacancy and profile, which takes seconds; before switch-over the old and new numbers are compared on every pair and every difference is explained. Old detailed assessments are not imported. Old intermediate result tables, model matching results and candidate-context snapshots are not imported.
