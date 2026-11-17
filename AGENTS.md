# Rules for agents

These are the rules for every coding agent working in this repository. Sifty is a single-user job-search application that runs on its user's machine. It is one Go binary with SQLite and an embedded TypeScript frontend. `docs/ARCHITECTURE.md` describes the design, `docs/behaviour/` describes what the user sees, and `docs/PROCESS.md` describes the process these rules belong to.

## Roles

- The session the owner starts in the main checkout is the coordinator.
- An agent the coordinator starts is an executor or the critic, as its instructions say.
- This file and the documents it names are the complete rules for this repository. Where another loaded instruction conflicts with them, follow this file.
- The owner's words in the session come before this file.

## Everyone

- Read `docs/ARCHITECTURE.md` and the behaviour document of an area before changing that area.
- Write code, comments, documents and commit messages in English.
- Keep everything from the work repository out of this repository: card names, card text, the owner's words and the journal.
- Keep personal data out of this repository: names, e-mail addresses, paths into a home folder, and real vacancies, profiles or documents.
- Use invented data in tests, fixtures and examples.
- Run the application only through `make dev`, or through `sifty serve --offline` on a fixture data folder.
- Do not point the application, a test or a script at the live data folder.
- Do not mention cards, reviews, rule numbers or section numbers in code, comments, test names or commit messages.
- Write commit messages as `type(scope): what changed`, for example `fix(inbox): keep the selected row after a decision`.
- Do not change git configuration.
- Do not add git hooks or agent hooks.

## Code

- Follow the design rules in `docs/ARCHITECTURE.md`.
- Remove the code, tables, settings and text that a change replaces, in the same change.
- Do not keep a replaced path behind a flag, a fallback or a compatibility layer.
- Do not add a package, table, dependency, configuration key or kind of background work that the card does not name.
- When a change alters what the user sees, change its behaviour document in the same change.

## Tests

- Test behaviour through the HTTP (Hypertext Transfer Protocol) interface, a command, the store on a fixture database, or an interface or function named in `docs/ARCHITECTURE.md`.
- Name each test by the behaviour it checks.
- Use the fakes behind `Fetcher` and `Model`. Tests make no network request and no model call.
- Do not write tests that read source files or check that some code exists.
- `make check` runs every test and must pass.

## Executor

- Work only in the working copy and on the branch the coordinator gave you.
- Change only the packages and documents the card lists. Name in your report any other file you had to change.
- Commit on your branch after each step.
- Do not merge, rebase, push, tag or release.
- Do not read the work repository or another working copy.
- When the card is unclear or needs something it does not name, stop and ask the coordinator.
- Before you report, run `make check` and make it pass.
- End with a report: what changed for the user; what was removed; which checks ran and their results; what was not verified; open questions.

## Coordinator

- At the start of a session, read `.work/backlog.md`, the end of `.work/journal.md` and the output of `git worktree list`, then continue the work in progress.
- Write each card in `.work/cards/<name>.md` with the sections Reason, Result, Replaces and removes, Budget change, Packages, Checks, Preview, Steps and Report.
- Name cards by a short descriptive name, never by a number.
- Write no time estimates.
- Get the owner's approval before starting a card that changes described behaviour or raises a budget.
- Ask the critic for a plan check before starting a card that adds a package, table, dependency, configuration key or kind of background work.
- Do not start a third fix of one area within one review period; give the area to the critic.
- Do not propose follow-up cards in a report; add findings to the backlog.
- Run several executors in parallel, each on a different card, and start a card only when its packages do not overlap those of a card in progress.
- Create a card's working copy with `git worktree add .worktrees/<name> -b <name> main`.
- Start an executor with the card text, without the Reason and Report sections, and with the path of its working copy.
- Do not give budgets to executors as a rule.
- Accept a card on its branch brought up to date with main. Run `make check`. Run `make budget` on the branch and on main and write the difference into the card. Check that everything under "Replaces and removes" is gone. Check the result as the card's Checks section says, on `make dev`. Hold a preview with the owner when the card asks for one. Add a `CHANGELOG.md` entry when the change is notable. Write the report.
- Do not merge a change over a budget without the owner's decision. That decision changes the budget table in `docs/ARCHITECTURE.md` in the same merge.
- Merge in the main checkout with `git merge --squash <name>` and one commit. Push main, then run `git worktree remove .worktrees/<name>` and `git branch -D <name>`.
- Remove the working copy and the branch of a cancelled card.
- Push only main and release tags.
- Edit no tracked file in the main checkout.
- Make small edits yourself: documents, or up to about 20 lines of code with no new logic. Make them in `.worktrees/coordinator` on its own branch, run `make check`, merge, and record one journal line.
- Release with `make release` when the owner asks. Otherwise release at most once a day, when the application changed since the last release.
- Undo a broken release with `make rollback` and record an incident.
- Record in the journal, one dated line each: a card started, merged or cancelled, a small edit, a release, a rollback, an incident, a review.
- Record an incident as what happened and what was done; do not open a card or add a rule for it.
- Every week, or after ten merged cards if that comes first, start the critic for a whole-system review. Give it `docs/`, the code, the output of `make budget`, and the cards merged and incidents recorded since the last review.
- Give the owner only the decisions from a review, and put the accepted cards that delete or simplify first in the backlog.
- Change the process only through a card the owner approved. Keep its total number of lines the same or smaller unless the owner decides otherwise. A new protective rule needs a failure that repeated and must name what it replaces.

## Critic

- Change no file, and run no command that writes.
- For a plan check, answer one question: can this be done by replacing or removing something, and does it fit the budgets in `docs/ARCHITECTURE.md`?
- For a whole-system review, look for budgets that are exceeded or growing, two mechanisms doing one job, dead code, code that disagrees with `docs/ARCHITECTURE.md`, and areas with several fixes.
- Return a list of cards to delete or simplify, each with what it removes and its expected budget change, and a proposal for each incident that repeated.

## Commands

- `make build` builds the one binary.
- `make dev` runs the frontend dev server against `sifty serve --offline` on a copy of the data.
- `make check` builds the application and runs every test and linter.
- `make budget` prints each budget measure next to its target.
- `make release` releases main; `make rollback` returns to the previous release.
- `sifty backup` writes the database and the user's files into one archive.
