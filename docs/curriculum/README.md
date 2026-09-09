# Rebuilding gitops-agent — a Go curriculum

A hands-on Go course built from this repo's own history: PRs #7–#11 (the
CLI stack — `update` and `install` subcommands, the bootstrap script, the
shim, the docs) were closed unmerged and preserved as `reference/*`
branches, and [curriculum.html](curriculum.html) turns that work into
twelve chapters of assignments to rebuild it from specs.

Open `curriculum.html` in a browser. Chapter 0 covers setup; the short
version:

```bash
# overlay the course files onto any branch, untracked, so they never
# land in an assignment PR:
git checkout claude/go-gitops-agent-curriculum-6v7ymk -- .claude docs/curriculum
git restore --staged .claude docs/curriculum
printf '.claude/\ndocs/curriculum/\n' >> .git/info/exclude
```

The interactive half lives in `.claude/skills/`:

- **tutor** — Socratic help while working an assignment; hints, never code.
- **grade** — invoke when a chapter feels done; gates → merit review →
  comparison against the reference branch.

This branch (`claude/go-gitops-agent-curriculum-6v7ymk`) intentionally
never merges to `main` — the course rides alongside the repo, not in it.
