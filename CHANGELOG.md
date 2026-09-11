# Changelog

All notable changes to `github.com/synthigy/go`. Follows
[semver](https://semver.org). Pre-1.0: breaking changes can land on minor bumps.

## 0.1.0 — unreleased

First public release.

### Added
- Zero-dependency `/data` client: OAuth client-credentials auth, the full CRUD
  operation set (search/get/sync/stack/slice/delete/purge/count), XSQL queries,
  SQL templates, schema introspection and the temporal `/history` API.
- Live data: record subscriptions, SSE streaming (`Listen`/`Observe`) and the
  typed `Watch` family with coalescing and backpressure modes.
- Single-client model — `Connect` installs a process-wide default and the
  package-level verbs operate on it.
- `cmd/synthigy-gen` — typed Go codegen from an `.xsql` operations document
  plus the IAM-filtered schema.

Standard library only. Requires Go ≥ 1.22.
