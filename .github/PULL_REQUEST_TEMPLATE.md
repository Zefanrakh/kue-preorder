Closes #<issue>

## Operator notes (written by the human, max 5 lines)

- What changed:
- Why:
- Key files:
- Risk:
- How to test:

## Checks

- [ ] `make check` green (integration tests with Docker running), or what was not run and why
- [ ] I ran and clicked through the result locally, where there is something to click
- [ ] The issue's acceptance criteria pass, and the must-not-break ones still do
- [ ] No existing test weakened, skipped or deleted
- [ ] Docs updated in this PR if behaviour changed (`docs/architecture.md`, the module's `docs/features/*.md`, an ADR for a new decision)
- [ ] Generated code committed (`sqlc`, `buf`); migrations are new files, never edits of merged ones
- [ ] No secrets, customer data or new dependency without the Lead's approval
- [ ] Base is `main`. Stacked PR: merge the PR below first, and merge this one only after GitHub shows base `main` here

Risk: high / low (high: money, stock, order status, auth or a migration; run `/zefan-sdlc:review-pr` first)

## Session log

<!-- from `make session-close --apply`: done and tests must be filled -->

- done:
- tests:
- next:
- lesson: none
