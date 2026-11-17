# Application and background work

What the user gets: when a vacancy enters Shortlist, the application finds the employer's own page for it, reads its application form and prepares a draft of every answer, filling known fields from My data and writing open answers with one model call. The user copies the answers into the form; the application never fills or submits a form itself. Background work never blocks the user, and the user always sees what it is doing.

## Entering Shortlist

Moving a vacancy to Shortlist starts, in the background:

- the search for the employer's page, when it has not run yet;
- the reading of the application form and the preparation of the draft;
- the Shortlist check (see `assessment.md`).

## Employer's page

- The search makes no model call. It tries, in order: the public boards of applicant tracking systems (Greenhouse, Lever, Ashby, Workable) under the company's likely board name, accepting an exact match of the normalised company name; a web search for `"<title>" <company>`, accepting only an applicant tracking system address or the company's own domain with a matching title; a web search for `<company> careers jobs` followed by a scan of the careers page.
- The automatic search stops after 30 seconds and the manual one after 120 seconds.
- At startup, shortlisted vacancies that were never searched are searched on the boards only, without web search, so no paid search is made at startup.
- A direct link once found is never replaced. A manual search is not started while another search for the same vacancy runs.
- The Apply button shows the result: "via employer site", "search for employer site" (which starts a manual search) or "searching…". For an aggregator listing it explains "Opens the listing on <name>…". Apply itself and the confirmation of the application are described in `inbox.md`.

## Application tab

**Reading the form.** The form is read by a browser without a model. The tab shows the state in words: "Reading the application form…", "Sign-in required…", "The site asked to slow down…", "No application form was resolved → Apply via the original listing", "No questions to answer → Apply directly", or the error. Fields that are not part of the application (site search, filters, newsletter sign-up) are left out. Requests of the browser are spaced per site, limited in number at once, and a site that blocked is left alone for a while.

**Reading the form again** keeps the answers: a field is matched by its id, or by its label when the label is unique. A question whose requirements changed is marked "Question requirements changed". A field that disappeared from the form is kept with the mark "removed" only when the user filled it; otherwise it is dropped. Questions the user added by hand are kept.

**Answers from My data.** Known fields (name, contacts, links, location, availability, compensation, application form answers and the like) are filled by code from My data and the profile, using a vocabulary of known label phrasings and the labels learned before. For a choice field the stored value is matched to the options; when none matches, the field carries the note "Profile says X; none of the options match". Sensitive fields are answered only when the user filled them in the profile. The consent checkbox is always left to the user.

**Labels the code does not know** are classified by one model call per form that sees only the labels, never the user's values. The result is remembered, and the same label never needs a model call again.

**Drafting.** The model writes only the cover letter and the open questions: free-text fields, unrecognised text fields, and choice fields My data cannot answer. All of them are drafted in one model call per form. The model receives the questions, the vacancy, the accepted data of My data and the profile, and the documents marked "Use as evidence" (see `profiles.md`).

- Progress reads "Drafting 4 answers", "Waiting for the model: N ahead" or "Loading model".
- An empty answer from the model is retried once.
- An answer that cites no evidence from the user's data is cleared with the note "No supporting candidate evidence". An answer outside a choice field's options is cleared. An answer longer than the field allows is marked as too long.
- The result reads "Drafted N of M answer(s); K failed: <reason>". One failed field does not discard the others.
- An answer the user edited is never overwritten by the model or by My data.

**An answer card** offers Copy, Edit and "Use this answer" (to accept a suggestion from My data or from Questions & Answers, marked "not yet saved"). Saving an answer that was changed in the meantime is refused with "This answer was changed elsewhere". A saved answer the user wrote or edited is added to Questions & Answers unless it is about this company only.

**A file field** lets the user choose a CV from Profile › Files, with Open and Download links and the text "Attach this file to the form yourself".

**Other actions:**

- **Write** drafts a cover letter when the form does not ask for one.
- **Paste** takes the text of a form the browser could not read; the model extracts its questions, and they are prepared like read ones.
- **Add a question** adds one by hand.

Diagnostic panels (questionnaire coverage, the technical record of form reading, preparation provenance) are shown only in debug mode.

## Background work

| Work | Started by | What the user sees | Model calls |
|---|---|---|---|
| Collection | interval, opening the application, "Collect jobs" (see `collection.md`) | progress in the header and on the Sources page | none |
| Reading vacancy texts | new or changed texts (see `assessment.md`) | the requirements counter's states | 1 per text |
| Recomputing Fits you | any change of vacancies, goals or My data | dimmed results until done | none |
| Shortlist check | entering Shortlist (see `assessment.md`) | the check on the vacancy page | 1 |
| Employer's page | entering Shortlist, the Apply button, startup | the Apply button's state | none; web search |
| Form reading and drafting | entering Shortlist, opening the tab when nothing was prepared, Write, Paste | the Application tab's states | 0–1 to classify labels, 1 to draft |
| Document analysis | Analyze or Retry (see `profiles.md`) | the document's state | 1 per chunk |
| Provider check | "Check" in Settings (see `settings-and-data.md`) | the answer of the check | 1 |

**Rules of background work:**

- A user's action goes ahead of background work and is never queued behind it.
- One model serves one call at a time; others wait and show "Waiting for the model: N ahead".
- One preparation per vacancy runs at a time, and one collection at a time.
- Reading a form never waits for a model.
- A failed or interrupted preparation keeps its state "failed" or "interrupted" with the reason and is retried only when the user asks; a restart does not retry it.
- When a provider is unavailable, background model work pauses and resumes by itself when the provider answers again.

## Not carried over

- One model call per answer; one call drafts all answers.
- "Not happy… Try another model"; the model is chosen in Settings.
- The revision history of drafts and stored snapshots of forms, which nothing read.
- Automatic retry of failed preparations at every restart.
- The cancel action of a preparation, which the interface never called and which did not stop the work.
- The detailed assessment queue with its switch and its unlimited retries (see `assessment.md`).
- The separate store of remembered answers used only by a self-check, and the feedback table that nothing read.
- Diagnostic panels shown to the user outside debug mode.

## Data from the old application

Each stored draft becomes the form of its vacancy with its fields: value, where the value came from, the user's edits kept as the user's, and the "removed" and "changed" marks and notes. Learned label phrasings are imported as they are. The employer's page address, its search state and the date it was found are copied to the vacancy. Draft revision histories, form snapshots, preparation operations and the old assessment queue are not imported; after switch-over, shortlisted vacancies without any form fields get their form read once, without drafting. Old run tables are not imported, except the time of the last successful collection, which keeps the collection interval. Before switch-over, every vacancy's number of fields, values and user-edited marks are compared with the old ones.
