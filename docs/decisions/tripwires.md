# Tripwires on decisions (Peter, 2026-11-25)

From the ADR check of 2026-11-25 (`decomplect/adr-check-2026-11-25.md` in the project files), which found 24 decision files with no way to check them.

1. Every decision file ends with a "Tripwires" section: a **Confirmation** line, how to check the code still follows the decision, and a **Revisit if** line, the fact that would make us rethink it (Q1).
2. Each Confirmation line is also an automated check, so a pull request that goes back on a decision fails (Q1).
3. All the checks live in one feature file, `src/hotline-proxy/features/decisions.feature`, one scenario per Confirmation line, named after its decision file, even where another feature file tests the same thing (Q2, Q3). It follows the rule that proxy behaviour is tested in Gherkin.
4. A new decision file gets its Tripwires section and its scenario in the same pull request.

## Tripwires

- Confirmation: every file in `docs/decisions` ends with a Tripwires section holding a Confirmation and a Revisit if line, and `decisions.feature` has a scenario named after each file.
- Revisit if the feature file grows too slow for `make test`, or most of its scenarios only repeat other feature files.
