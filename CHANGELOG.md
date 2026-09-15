# Changelog

All notable changes to `github.com/synthigy/go`. Follows
[semver](https://semver.org). Pre-1.0: breaking changes can land on minor bumps.

## 0.1.0 — unreleased

### Added
- **`Deploy` and `Destroy`** — `Client.Deploy` / `Client.Destroy`, the
  `DeployAck` type, `OpDeploy` / `OpDestroy` builders, and the package-level
  facade mirrors. The caller that needed them — the RLS demo, flipping guards
  on and off — hand-rolled an HTTP POST with
  `Content-Type: application/transit+json` and its own result parsing, under a
  comment explaining that "Go has no transit encoder". It never needed one:
  the export travels verbatim and the server decodes it. `Destroy` is `delete`
  on the `dataset` meta-entity, and the deploy ack carries the dataset xid it
  takes, so a caller holding nothing but the export can still tear down what
  it deployed. Both are scope-gated server-side (`dataset:deploy` /
  `dataset:delete`, which only the Dataset Developer role carries).

  The package facade landed one pass ahead of the methods it calls, which left
  `global.go` referencing an undefined `DeployAck`, `c.Deploy` and `c.Destroy`
  — the module did not build at all between those two passes. Nothing was
  released in that window.

### Changed
- **The platform audience is now the default; nobody configures it.** A
  `client_credentials` mint naming no audience resolves to the identity-only
  OIDC audience, which `/data`, `/schema`, `/history`, `/logs` and
  subscriptions all reject — so every user had to set `SYNTHIGY_AUDIENCE` to a
  constant they could not look up, since the server does not advertise it in
  discovery. The failure was a bare 401 that said nothing about audiences.
  This SDK is the client for the platform API, so that is what it now mints
  for. Minting for a different API stays a per-call argument. The environment
  variable remains as an escape hatch.

### Fixed
- **Codegen emitted unexported struct fields, and `go vet` failed the whole
  package.** Field names come from the schema's `pascal` skin, which is
  authored data rather than a Go identifier: an attribute skinned `rucOTF`
  became a field `rucOTF`, which `encoding/json` skips without a word — the
  column silently never round-trips — and which `go vet` rejects for carrying a
  json tag. Every entity/attribute/relation name derived from a skin now has
  its first rune upper-cased. Found by generating against a real model and
  running `go vet`, which nothing in the repo did before.


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
