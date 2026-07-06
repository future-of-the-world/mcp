# AGENT.md

Notes and rules for AI agents working on this repository.

## Hard Rules

1. **No direct commits on `master`.** Every change to the `master` branch must be submitted as a pull request (PR) in Forgejo and merged via the normal review/merge flow. Do not commit directly to `master` and do not push directly to `origin/master`.

2. **Never skip CI/CD on your own commits.** Every commit and PR an agent authors must run the full Woodpecker pipeline. Do not add `[skip ci]`, `[ci skip]`, `[no ci]`, or any equivalent directive to commit messages, PR titles, or PR bodies that you author.

   > **Note:** `[skip ci]` commits may legitimately exist in the repository's history because the CI/CD system itself uses them for automated coverage-badge updates (see `.woodpecker/coverage-store.yaml`). These are valid and must not be rewritten. The rule above is about **new commits the agent authors** — rebases and cherry-picks that preserve an existing commit's message are fine, but a fresh commit you write from scratch must not skip CI.

## Soft Rules

*(TBD — additional agent conventions will be added here as the project matures.)*

## `.issues/` Folder Rules

The `.issues/` directory holds per-issue documentation (`description.md` and optional supplementary files). The rules below come from how the project is operated in practice — issue files travel with the work, status is git state, and the main checkout is reserved for `master` and the agent's own work.

1. **Create issue files in a worktree, never in the main checkout.** When opening a new issue, first create a worktree cut from a clean `master` (use `issue-creator`), then write `.issues/<slug>/description.md` inside that worktree and commit it on the `<slug>` branch. The file must not be created in the main working directory and must not be committed on `master` directly.

2. **Do not track status in the issue file.** The `description.md` has no `**Status:**` field, no checked-off acceptance criteria (`- [x]`), and no per-dependency status parentheticals (e.g. `(open — PR #51)`). The branch's git state is the source of truth: a `<slug>` branch that exists but is not in `master`'s history is **in progress**; a `<slug>` branch whose tip is in `master`'s history is **done**.

3. **An issue file in `master` implies the changes it describes are also in `master`.** If you see a `.issues/<slug>/description.md` in `master`, the corresponding code changes must also be present. If the work for a `<slug>` is not in `master`, its `description.md` must not be in `master` either — keep it on the branch until the work merges.

## CI/CD and Master Sync Reference

For the operational workflow (waiting for CI, monitoring pipelines, syncing worktrees after `master` advances), see the project-local agent skills:

- `.agents/skills/issue-implementer/SKILL.md` — the primary skill for PR and merge workflows; contains the full **CI/CD Notes** and **Master Sync Protocol** sections.
- `.agents/skills/issue-creator/SKILL.md` — for branch and worktree creation, with a pointer to the master-sync rules.
- `.agents/skills/issue-reader/SKILL.md` — for reading cross-worktree state, with a pointer to the master-sync rules.
