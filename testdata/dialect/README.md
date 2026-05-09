# Dotenv dialect fixtures

This directory holds vendored test fixtures used by `TestDialectCompat` to verify our dotenv parser's behavior across the four major dialects in the wild. Fixtures are committed to the repo (not cloned at build time) because none of these projects publish a versioned standalone test corpus.

## Subdirectories

- `bkeepers/`  — Ruby [bkeepers/dotenv](https://github.com/bkeepers/dotenv) test cases. Sync'd manually from the upstream `spec/fixtures/`.
- `motdotla/`  — Node.js [motdotla/dotenv](https://github.com/motdotla/dotenv) test cases. Sync'd manually from the upstream `tests/`.
- `compose/`   — [Docker Compose](https://docs.docker.com/compose/environment-variables/env-file/) `.env` syntax. Hand-curated from the published spec.
- `foreman/`   — [Foreman](https://github.com/ddollar/foreman) / Heroku-local dialect. Hand-curated from the foreman README and known dialect divergences (Foreman has no public `.env` test suite).

## Re-syncing

When upstream releases a new major version with parser changes, manually copy the relevant fixtures and update this README's "Sync'd from" pointer. Each fixture is annotated with an `_expected.json` sibling describing the expected `map[string]string` parse result and any expected error class.

## Fixture format

Each `*.env` file is a fixture. Optionally, a `*_expected.json` sibling specifies expected parse output:

```json
{
  "result": { "FOO": "bar", "BAZ": "qux" },
  "error":  null
}
```

Or for fixtures expected to fail:

```json
{
  "result": null,
  "error":  "ErrSyntax"
}
```
