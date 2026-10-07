# AGENTS.md

## Scope

These instructions apply to the entire repository. Follow the current repository documentation, implementation notes, and nearby code. If instructions conflict, stop and make the mismatch explicit before changing scope.

## Working rules

- Always use Ponytail skill, Graphify skill and MCP. 
- Inspect the relevant documentation and nearby code before editing. Preserve unrelated user changes.
- Keep changes small and focused. Update adjacent documentation when behavior, configuration, migrations, or operational steps change.
- Follow established repository conventions. If none exist yet, use idiomatic code, clear ownership, and explicit error handling.
- Do not declare work complete while known failures, skipped required checks, or unresolved scope questions remain hidden.
- Never commit secrets, credentials, tokens, or real personal data. Use documented environment variables and safe local fixtures.

## Test rules

- If an agent writes code that needs test coverage, the agent must add or update appropriate test files in the same change.
- Run the relevant tests after every meaningful code change. Start with the narrowest affected test, then run the broader applicable suite before handoff.
- For Go changes, run formatting and the configured lint/test checks. When available, the baseline is `gofmt`, `go vet ./...`, `staticcheck ./...`, and `go test ./...`.
- For database changes, run the relevant migration and integration tests. Include blank-to-head and repeat/idempotence coverage when migrations are affected.
- For infrastructure or configuration changes, validate the affected config files and smoke-test the relevant service or health endpoint when the environment is available.
- Add regression tests for bug fixes and tests for new behavior. Never weaken or delete a test just to make a change pass.
- Record every check in `WORKLOG.md`. If a required check cannot run, record the exact command, reason, and remaining risk.

## Worklog rules

- Agents must update `WORKLOG.md` in the same change after meaningful codebase work. Meaningful work includes features, fixes, migrations, refactors, dependency/configuration changes, and significant documentation or operational decisions.
- Insert each entry at the top of the `Entries` section so entries remain newest-first. Use an ISO 8601 UTC timestamp and the documented entry template.
- Keep entries factual: summarize the outcome, list material files/components, record validation, and note follow-ups or risks.
- Do not log routine exploration, read-only inspection, or trivial formatting by itself. Do not rewrite older entries except to correct a factual error.
  
## Graphify Rules

* **Codebase Analysis:** Before running global text searches across the workspace, always parse `graphify-out/graph.json` to understand the structural layout.
* **Dependency & Import Maps:** Rely exclusively on `graphify-out/graph.json` to trace cross-file imports, function calls, and module relationships.
* **Token Efficiency:** Do not read raw source files sequentially to find connections; the pre-compiled graph data in `graph.json` contains all architectural connections.
* **Workspace Updates:** After making structural changes or adding new components, remind the user to run `graphify . --code-only` in their terminal to update the map and `graphify cluster-only .` to generate GRAPH_REPORT.md and name communities.
