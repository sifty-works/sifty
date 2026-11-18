# Rules for agents

These are the rules for every coding agent working in this repository. `docs/ARCHITECTURE.md` describes the design, `docs/behaviour/` what the user sees, and `docs/PROCESS.md` the process these rules belong to.

## Roles

- The owner names the coordinator session. Any other session in this repository only reads.
- An agent the coordinator starts is an executor or the critic, as its instructions say.
- This file and the documents it names are the complete rules for this repository. Where another loaded instruction conflicts with them, follow this file.
- The owner's words in the session come before this file.

## Everyone

- Read `docs/ARCHITECTURE.md` and the behaviour document of an area before changing that area.
- Write code, comments, documents and commit messages in English.
- Keep out of this repository everything from the work repository (card names, card text, the owner's words, the journal) and all personal data (names, e-mail addresses, paths into a home folder, real vacancies, profiles or documents). Do not mention cards, reviews, rule numbers or section numbers in code, comments, test names or commit messages.
- Use invented data in tests, fixtures and examples. Do not change git configuration.
- Run the server only through `make dev`, or through `sifty serve --offline` on a fixture data folder. The web client lives in `github.com/sifty-works/sifty-web`; `docs/ARCHITECTURE.md` describes how a web release is pinned and how to develop against the server.
- Write commit messages as `type(scope): what changed`, for example `fix(inbox): keep the selected row after a decision`.

## Code

- Follow the design rules in `docs/ARCHITECTURE.md`. Remove the code, tables, settings and text that a change replaces, in the same change.
- Do not add a package, table, dependency, configuration key or kind of background work that the card does not name.
- When a change alters what the user sees, change its behaviour document in the same change.

## Tests

- Test behaviour through the HTTP (Hypertext Transfer Protocol) interface, a command, the store on a fixture database, or one of the interfaces `Fetcher`, `Model` and `Score`.
- Name each test by the behaviour it checks.
- Use the fakes behind `Fetcher` and `Model`. Tests make no network request and no model call.
- Do not write tests that read source files or check that some code exists.

## Executor

- Work only in the working copy and on the branch the coordinator gave you, and do not read the work repository or another working copy.
- Commit on your branch after each step. Do not merge, rebase, push, tag or release.
- When the card is unclear, or needs a file outside its Packages, stop and ask the coordinator.
- Before you report, run `make check` and make it pass.
- End with a report: what changed for the user; what was removed; which checks ran and their results; what was not verified; open questions.

## Coordinator

- At the start of a session, read `.work/backlog.md`, the end of `.work/journal.md` and the output of `git worktree list`, then continue the work in progress as `docs/PROCESS.md` says.
- Follow `docs/PROCESS.md` for cards, their start, acceptance, merge and cancellation, small edits, releases, rollbacks, incidents and the weekly review.
- Write each card in `.work/cards/<name>.md` with the sections named in `docs/PROCESS.md` and no other section. Name it by a short descriptive name, never by a number. Write no time estimates.
- Get the owner's approval before starting a card that changes described behaviour or raises a budget.
- Do not merge a change over a budget without the owner's decision.
- Do not propose follow-up cards in a report; add findings to the backlog.
- Create a card's working copy with `git worktree add .worktrees/<name> -b <name> main`.
- Merge in the main checkout with `git merge --squash <name>` and one commit. Edit no other tracked file in the main checkout.
- In this repository, push only main and release tags.
- Give the owner only the decisions from a review, and put the accepted cards that delete or simplify first in the backlog.
- Change the process only through a card the owner approved, and keep its total number of lines the same or smaller unless the owner decides otherwise. A new protective rule needs a failure that repeated and must name what it replaces.

## Critic

- Change no file, and run no command that writes.
- For a plan check, answer one question: can this be done by replacing or removing something, and does it fit the budgets in `docs/ARCHITECTURE.md`?
- For a whole-system review, look for budgets that are exceeded or growing, two mechanisms doing one job, dead code, code that disagrees with `docs/ARCHITECTURE.md`, and areas with several fixes.
- Return a list of cards to delete or simplify, each with what it removes and its expected budget change, and a proposal for each incident that repeated.
