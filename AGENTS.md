# Project guidance

Build and test commands are in [Development](docs/development.md).

- Keep changes focused. Prefer simple functions and existing packages.
- Add abstractions and dependencies when they solve a concrete problem.
- Explain intent and protocol constraints in comments. Avoid narrating the code.
- Use Conventional Commits. PR descriptions should explain the resulting behavior
  and how it was verified.
- Run the relevant checks and report what actually ran. Never present a skipped
  interoperability test as a passing one.

## Docs and templates

Run the `unslop` skill on every new or edited documentation file and template
before committing or publishing. This includes README files, project guidance,
issue and PR templates, and release notes. If the skill is unavailable, report
the missing review and stop before committing or publishing the affected text.

Write for the person using or maintaining the project. State the behavior,
reason, and verification plainly. Keep claims within what has been tested.

## Protocol changes

Preserve the conformance fixtures as fixed expectations. A change in server output
needs an explanation and a test; do not regenerate expected frames from that output.
Keep optional Rex interoperability checks separate from tests of this server.
