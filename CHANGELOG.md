# Changelog

All notable changes to `github.com/synthigy/go`. Follows
[semver](https://semver.org). Pre-1.0: breaking changes can land on minor bumps.

## 0.2.0

### Added
- **Bulk writes: `SyncMany` / `StackMany`** (Client and package level) send
  every record in ONE operation and return a `WriteResult{Count, Records}`
  (`Records` only with `Returning()`). `OpSync`/`OpStack` take one record or
  a slice. `synthigy-gen` emits typed `SyncMany`/`StackMany` per entity. A
  Go bulk import used to be one operation per record.
- **Browser login: `LoginStart` / `LoginComplete` / `LoginCancel`** (Client
  and package level) — OIDC authorization code + PKCE for a confidential
  client, same contract as the JS and Python SDKs. In-flight logins live in
  `Config.LoginStore` (`NewMemoryLoginStore` for one process); the user's
  `XID` comes from the id_token. `ClientSecret` is now kept even alongside a
  static `Token`, since the code exchange needs it.
- **`synthigy-gen` generates `@batch`** as `gen.API.<Batch>(ctx, params)`:
  every member in ONE request, and a struct with a typed result field and an
  `Err` field per member, so a failed member leaves the others intact. It used
  to be dropped without a word.
- **`OpQuery`, `ResultAs`, `ResultOneAs`** — build an XSQL read for `Exec`,
  and decode one `Exec` result into typed rows (or its `*Error`).
- **`Config.Endpoint` defaults to `SYNTHIGY_ENDPOINT`.** Under
  `synthigy exec` that and the identity are already set, so
  `synthigy.Connect(synthigy.Config{})` is the whole setup. No endpoint
  anywhere is `NO_ENDPOINT` (category `validation`, not retryable); it was
  `INVALID_BODY`.
- **`Compile`** — `Client.Compile` / the package-level mirror post an XSQL
  source to `POST /compile` and return the wire `Op` the engine would execute,
  without executing it. The compiler is the authority on the wire format, so
  this is how you take programmatic control of a query instead of hand-writing
  the map: edit what comes back and pass it to `Exec`. `params` bind exactly as
  on `Query`, so the result IS what the engine receives.

### Changed
- **`synthigy-gen` no longer falls back to `http://localhost:7887`.** With no
  endpoint passed and no `SYNTHIGY_ENDPOINT`, a run that needs the server
  fails with `NO_ENDPOINT`, as the client does; offline runs from a current
  `ops.ir.json` are unaffected.
- **`synthigy-gen`:** each `.xsql` file is sent to `describe` separately (`sources: [{path,
  source}]`) instead of one concatenated text. Concatenation leaked the
  first file's buffer-level `@namespace` into every later file and dropped
  the others'. Needs an engine with per-file `describe`. A `(namespace,
  name)` declared twice across files now fails with `DUPLICATE_OPERATION`,
  naming both files; a `@batch` naming an op from another file fails with
  `BATCH_MEMBER_UNRESOLVED`. Both are `validation` in the error table.
  New `OpDescribeFiles` builds that request. A failed `describe` now shows
  the server's message; it used to say "describe returned no IR".

### Fixed
- **`synthigy-gen` skips `@sync` / `@stack` / `@delete` operations** with a
  `skipped …` line naming the schema-derived write to call instead, as the
  other four SDKs' generators do. It used to exit on "unknown op kind".
- **Package-level `Onboard` / `OnboardComplete`.** They were the only `Client`
  methods with no mirror, so `synthigy.Connect` users had to reach for
  `Default()`.
- **`synthigy-gen` no longer generates from a stale IR.** After a `.xsql` edit
  it warned and emitted the old operations; it now refreshes the IR from the
  server, and fails naming the edit when it cannot reach one — the same as the
  other SDKs' generators. `-check` is unchanged.
- **`-xsql` defaults to `./xsql`**, the directory every example uses (was
  `./synthigy`).

## 0.1.0

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
