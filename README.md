# Sifty

Sifty is a personal job-search application that runs on your own machine. It collects vacancies from the job sites you choose, shows each one once with clean readable text, scores how well it fits the goals of each of your search profiles, and prepares draft answers for the application forms of the vacancies you shortlist. You copy the answers into the form yourself; Sifty never submits anything.

Your data stays in one folder on your machine. Sifty sends requests only to the job sites you list, to the web search providers you configure, and to the model providers you choose: a local model through Ollama or any OpenAI-compatible server, a remote OpenAI-compatible service with your own API key, or the Claude or opencode command-line tools. Scoring is done by code; a model is used only to read the requirements of a vacancy once, to check a shortlisted vacancy, and to draft application answers.

## Status

Sifty is being rewritten from scratch. The documents come first and the code follows them; there is no code in this repository yet.

## Documents

- `docs/behaviour/` describes what the user sees and gets, one document per area:
  - `collection.md`: sources, collection runs, vacancy text, industries, duplicates;
  - `assessment.md`: the Fits you score, requirements, the Shortlist check;
  - `inbox.md`: the lists, search, filters, decisions, the vacancy page;
  - `profiles.md`: My data, profiles, documents and suggested facts;
  - `application.md`: application forms, draft answers, background work;
  - `settings-and-data.md`: settings, model providers, where data lives, import of the previous version.
- `docs/ARCHITECTURE.md` describes how the code, the data and the background work are arranged, and the size and cost budgets the code must stay within.
- `docs/PROCESS.md` describes how the repository is developed.

## License

Sifty is licensed under the [Elastic License 2.0](LICENSE).
