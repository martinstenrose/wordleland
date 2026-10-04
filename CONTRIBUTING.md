# Contributing

All changes go through a branch and a pull request.

Branch naming follows Conventional Commits prefixes:

- `feat/` — new feature
- `fix/` — bug fix
- `docs/` — documentation only
- `chore/` — maintenance (deps, config, tooling)
- `refactor/` — code restructuring without behaviour change

Commit messages:

- Subject line: `<type>(<scope>): <short imperative summary>`, no period.
  `<type>` matches the branch prefixes above. `<scope>` is the affected
  package or area (e.g. `web`, `bridge`, `ingest`, `cli`, `store`, `wordle`,
  `stats`, `auth`, `config`); omit it for repo-wide changes with no single
  owning area.
- Body with bullet points for non-trivial commits, describing only what
  changed in the repo.
- The body explains the reasons, not the sequence of work. Nobody reading it
  later needs to know what was tried first and abandoned, and a change that
  was made and then reverted does not belong in the history at all.

**Every commit builds on its own.** Not just the tip: `go build ./...` has to
pass at each one, or bisecting is guesswork. A rename spread across packages
is where this breaks — the commit that moves a package must not leave an
earlier one referring to it.

**The history `main` gets is the story of the change, not the story of
reaching it.** A pull request usually goes through review rounds — a fix
corrected, an approach reworked, a test adjusted after the first one missed
something. None of that belongs in `main` once the PR merges. A pull
request can hold several commits when each is its own change — a feature,
a fix, a refactor — but a commit that fixes something introduced earlier
in the same pull request is folded into the commit it fixes (`git commit
--fixup`, then `git rebase -i --autosquash`) before the pull request is
marked ready. The repository allows no squash merge, so what lands is the
commits as they are: one coherent change each, building on the last, not
a transcript of how review went.
