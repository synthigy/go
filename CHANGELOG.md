# Changelog

All notable changes to `github.com/synthigy/go`. Follows
[semver](https://semver.org). Pre-1.0: breaking changes can land on minor bumps.

## 0.5.1

### Added
- **Record locks (engine 0.5.6+):** `Claim`, `Renew` and `Release`, with `OpClaim` / `OpRelease`. `@claim` documents generate typed functions; `@release` documents are skipped with a pointer to the client method.
- **Codegen:** `@slice` documents generate a method returning a relation-name-to-boolean map (empty when the engine sends null), also as a `@batch` member; `@purge` generates a list method. New `QuerySlice` and `ResultSlice`; `Slice` returns an empty map instead of nil on a null result.

## 0.5.0

### Breaking
- **`Observe` is gone (method and package-level); use `Watch`.** It posted its own subscription set, and the
  engine keeps one set per client, so it overwrote the set every watch shared. For the same loop:
  `w, _ := client.Watch(ctx, synthigy.WatchInterest{Records: xids}); for ev := range w.Events() {...}`. Its
  `Backfill` / `BackfillLimit` options live on as `Watch` options. `Descriptor`, `RecordsDesc` and `SubKey`,
  which only served it and the raw subscription calls, are gone too.
- **`Subscribe`, `Unsubscribe`, `SubscribeModel`, `UnsubscribeModel`, `SetSubscriptions` and
  `ClearSubscriptions` are unexported** (methods and package-level). Each replaced the client's whole
  subscription set behind the watch multiplexer's back. Declare interest with `Watch`; `Subscriptions` still
  shows what the engine holds.

### Added
- **Each event stream has its own subscription set** (engine 0.5.5+). Every connection gets a fresh stream id
  (`/data/events?stream=…`, also on the `sse/open` event as `Event.Stream`) and each subscription POST names
  it, so two processes on one client id no longer overwrite each other's watches. `Subscriptions` asks for the
  watch stream's set. An older engine ignores the id and keeps one set per client.
- **`Watch(ctx, interest, synthigy.Backfill(true))`** replays the record events missed during a reconnect from
  `/history` (asked per watched record, at most `BackfillLimit` rows each, default 1000, marked
  `WatchEvent.FromBackfill`), then emits `connection/resumed`. Live events that arrive during the replay are
  held and delivered after it, also when the replay fails.
- **A busy engine is retried for you.** `SERVER_BUSY` (HTTP 503 from an overloaded engine) is resent up to
  3 attempts in all, waiting `Retry-After` (at most 10 s) with ±50% jitter, never past the client's `Timeout`; cancelling the context stops the wait. The request never
  ran, so this is safe for writes too. When all 3 are refused you get the error: category `rate_limit`, retryable.

### Fixed
- **A subscription POST that outlives its stream's session no longer marks watches `Subscribed()`.** A POST
  in flight when `sse/close` ended the stream could still succeed, and the covered watches were marked
  subscribed although the engine had dropped that stream's set; they now wait for the next open's POST.
- **A watch closed while another opens no longer sends the new watch a false `subscription/rejected`.** A
  subscription POST cut off by the stream closing was reported as a refusal to every current watch.
- **A watch no longer reports `Subscribed()` while no event stream is open.** With the stream gone (closed,
  or the engine answered `STREAM_NOT_FOUND`) a watch whose interest matched the last posted set was marked
  subscribed although the engine held nothing; it now waits for the next stream to open and post.
- **A watch registered while the event stream reconnects no longer reports `Subscribed()` early.** Between a
  dropped connection and the next `sse/open` the multiplexer kept the dead stream's id, so a watch whose
  interest matched the last posted set was marked subscribed although the engine held nothing. `Listen` now
  emits `{Type: "sse/close", Stream: id}` when an opened session ends, and the watch waits for the next open.
- **Live queries re-run on `connection/resumed`.** `WatchQuery`, `WatchQueryXSQL` and `WatchSqlTemplate`
  passed the sentinel through but did not refresh, so a dropped connection left them stale until the next
  change.
- **A subscription set is remembered as sent only once the engine took it.** The multiplexer recorded the union
  before posting, so after a refused POST the same union was never sent again.
- **The multiplexer never posts to a stale or missing stream.** It waits for the next `sse/open` when no stream
  is open, a late `sse/open` from a closed stream no longer takes over, a flush queued for a closed stream
  goes to the current one, and a `STREAM_NOT_FOUND` answer is not a refusal: it clears the stream id only if
  that is still the current one, so it can't wipe a newer stream's id.
- **A busy resend gets only what is left of the client's `Timeout`.** Each resend used to get a fresh full
  timeout, so a call could run to about twice `Timeout`; now the whole call stays within it.
- **A stale event stream's refusal no longer stops a newer one.** If the last watch closed and a new one opened
  before the old stream finished failing, the old stream cancelled the new stream and marked it stopped. It also
  no longer sends its refusal to, or marks as subscribed, the watches of the newer stream.
- **A watch on every operation is no longer narrowed by another watch's op filter.** The shared subscription
  sent the union of every watch's ops, so one watch filtered to `insert` stopped updates and deletes for watches
  that wanted all of them. Any watch with records and no op filter now turns the filter off.
- **A watch after a refused event stream opens a new stream.** A refusal used to leave the watch multiplexer
  marked running, so later watches got no events. Also fixed a data race between closing the client and closing
  the stream; cancelling during a busy wait now returns `ctx.Err()`.
- **45 engine error codes now carry their real category** instead of `internal`: bad shapes, `INVALID_*` and
  missing arguments are `validation`; `REF_NOT_FOUND`, `UNKNOWN_ATTRIBUTE`, `UNKNOWN_ROLE` and `DOC_NOT_FOUND` are
  `not_found`; `GUARD_VIOLATION`, `CONSTRAINT_VIOLATION`, `DEPENDENT_OBJECTS`, `HOOK_REFUSED`, `LOCKED` and
  `SCHEMA_CONFLICT` are `conflict`; `DB_UNAVAILABLE` is `internal` and retryable. `DB_ERROR` stays unclassified
  and not retryable; the engine's own `retryable` flag still wins.
- A 403 on the event stream ends `Listen` whatever its code, as in Python and
  C# (it kept reconnecting on, say, `ENTITY_FORBIDDEN`), and every `Watch` gets
  `subscription/rejected` with the error when the stream is refused (the
  watches went quiet).
- Watches keep receiving events after the event stream reconnects. The engine
  drops an identity's subscriptions when its stream closes, and the multiplexer
  did not send the unchanged set again, so every `Watch`, `WatchQuery` and
  `WatchSqlTemplate` went quiet after the first reconnect.

## 0.4.0

### Added
- **`Error.Doc`, `Error.Rule` and `Error.Attribute`**, set when the engine sends
  them, like `Hint`: `Rule` is the rule that refused and `Doc` its manual
  section (e.g. `errors/relation_data`, readable at
  `GET /docs/errors/relation_data`). Every error a call returns keeps them,
  `Exec` results (`OpResult.Error`), `ResultAs` / `ResultOneAs` and generated
  batch members included; empty and left out of JSON when absent.
- **`WatchEvent.Err` on `subscription/rejected`**: the error the refused
  subscribe or refresh returned (an `*Error` for an engine refusal, with
  `Code`, `Hint`, `Rule`, `Doc`), beside the unchanged `Reason`.

### Fixed
- `Destroy` returns `true` once the engine accepts it, also when the engine
  answers `null` (it returned `false`).
- `Onboard` / `OnboardComplete` answered an engine error object (a router 404, a
  500, a 503 `MODULE_NOT_LOADED`) with `HTTP_ERROR`; they return the engine's
  error, its code and fields included. Bare-string codes are unchanged.
- `synthigy-gen` describe failures show the engine's position, hint and manual section
  (`docs: <endpoint>/docs/<doc>`), not only the message and code.

## 0.3.0

Needs engine v0.5.0 or later.

### Breaking
- **`Config.ActingAs` is gone.** The client runs as itself; a call acts as a
  user only with the `ActingAs(xid)` option.

### Added
- **Acting as a user on files.** `ActingAs(xid)` on every `Files()` call (`Put`, `PutNew`, `Download`, `Zip`,
  `Info`, `List`, `Search`, `Delete`, `Move`, `Folder`, `Share`, `Unshare`,
  `Access`, `Versions`, `Restore`, `Link`, `Links`, `Unlink`, `Serve`); sent as
  `X-Acting-As`. Methods that took no options now take `opts ...Opt`.
  A trusted BFF works on files as the user, like `/data`.

### Fixed
- A write right after `WatchQuery` / `WatchQueryXSQL` / `WatchSqlTemplate`
  returned could be missed: they returned before the engine held the
  subscription. They now wait for it and refetch once. New
  `WatchHandle.Subscribed()`.
- Events that reached a `Watch` before its first `Events()` call were dropped (no
  listener yet). The handle now buffers from the start and the first `Events()`
  call gets that buffer.
## 0.2.4

Needs an engine with `POST /files/zip` (after v0.4.1).

### Breaking
- **`_count` / `_agg` are gone** (engine after v0.4.1 refuses them).
  Write an operation per value: XSQL `votes: count(ratings)`,
  `rating: avg(ratings.value)`; on the wire an `{alias, op, args?}` entry
  on the relation key. The alias is the result key and can be sorted by.
- `synthigy-gen` types an operation as a plain field (counts `int`, never null;
  `avg` float; `sum`/`min`/`max` the field's type). The `_count` / `_agg`
  map fields are gone; regenerate.

### Added
- **`Files().Zip(ctx, files, folders)`**: one zip of the files and of
  everything inside the folders, read as you, streamed as a `FileDownload`;
  the client timeout does not apply. `ZIP_TOO_LARGE` joins the error codes.
- **`Files().Link(ctx, file, ...)`** (`LinkExpires`, `LinkMaxDownloads`,
  `Inline`), `Links(ctx, file)`, `Unlink(ctx, linkXID)`: download links for
  people without an account, where the folder's `CanLink` allows them;
  `SetCanLink` / `InheritCanLink` for `Folder`, `Folder.CanLink`,
  `FolderSettings.CanLink`. `LINKS_DISABLED`, `LINK_NOT_FOUND` and
  `INVALID_EXPIRES` join the error codes.

### Fixed
- `Config.Timeout` no longer cuts file bytes short: `Files().Put`, `PutNew`
  and `Download` (like `Zip` and `Serve`) are bound by their `ctx` alone.
- `FILES_MIGRATING` (uploads paused while the server moves files to a new store) is now category `internal` and retryable; it was unknown, so never retryable.

## 0.2.3

Needs engine v0.4.1 or newer for files.

### Added
- **`Client.Files()`**: `Put` (a path creates or replaces the file there,
  its folder must exist; an xid replaces that file; `IfMatch`, `ContentType`),
  `PutNew` (a new file in no folder), `Download` (`ByteRange`, `IfNoneMatch`,
  `AtVersion`), `Info`, `List` (`InFolder`: a folder's `Listing{Files,
  Folders}`; `NoFolder`: files in no folder; neither: every file you can see;
  `Matching` (words in order, a space for anything between), `Limit`,
  `Offset`), `Search` (folders and files whose paths hold the words in order,
  shortest path first), `Delete` (a file, or an empty folder), `URL` (a link
  without a request, `Inline`), `Move` (a file or a folder), `Folder` (made
  with every missing folder above it; `SetPublic`, `SetCache`,
  `InheritPublic`, `InheritCache`), `Share`, `Unshare`, `Access`, `Versions`,
  `Restore` and `Serve` (an `http.Handler`-style answer that passes Range and
  304 through). A file is a path or an xid; a folder a path ending in `/` or
  an xid. Types `FileInfo`, `Folder`, `FolderSettings`, `Listing`,
  `SearchResults`, `SearchHit`, `FileVersion`, `FileDownload`, `FolderAccess`.
  New error codes: `FILE_NOT_FOUND`, `FOLDER_NOT_FOUND`, `FILES_UNAVAILABLE`,
  `AUDIT_UNAVAILABLE`, `VERSION_EXPIRED`, `NOT_FOUND` → `not_found`;
  `FILE_NAME_TAKEN`, `FILE_CHANGED`, `FILE_NOT_READY`, `FOLDER_NAME_TAKEN`,
  `FOLDER_NOT_EMPTY`, `FOLDER_CYCLE` → `conflict`; `NAME_REQUIRED`,
  `INVALID_NAME`, `INVALID_ACCESS`, `INVALID_PATH`, `INVALID_SETTINGS`,
  `INVALID_QUERY`, `METHOD_NOT_ALLOWED`, `INVALID_SLICE`, `FILE_TOO_LARGE`,
  `UNSUPPORTED_MEDIA_TYPE`, `RANGE_NOT_SATISFIABLE` → `validation`. Needs a
  server with the files module and File System 0.3.10.

### Changed
- Error categories for the deploy checks: `SYSTEM_DATASET_ENGINE_OWNED` and
  `SYSTEM_ENTITY_ALTERED` → `iam` (a superuser may); `ENTITY_NAME_CONFLICT`,
  `FIELD_KEY_CONFLICT` and `IMMUTABLE_VERSION_ALTERED` → `validation`. They used to
  fall back to `internal`.
- `UNKNOWN_TREE_RELATION` (the engine refuses an `on` of a tree op that is not a tree relation of
  the entity) → `validation`.
- More codes categorized: `ENGINE_OWNED` (a meta-model row or field only deploy and
  recall write) → `iam`; `SYSTEM_ENTITY_RLS`, `RLS_GUARD_BROKEN` and
  `VERSION_NAME_TAKEN` → `validation`; `MODEL_BUSY` (another deploy, recall or destroy
  is running; retry when it finishes) → `conflict`.

## 0.2.2

### Added
- **`Whoami` / `Client.Whoami(ctx, ActingAs(xid))`** (and `OpWhoami`,
  `WhoAmI`) — who a call runs as: xid, name, active, superuser, roles
  (group-conferred included), groups, and `Client` `{ID, Trusted}`, where
  `Trusted` means this client may act as users. Needs engine v0.2.11 or newer.

## 0.2.1

### Fixed
- **A login may take 10 minutes, not 5** (`NewMemoryLoginStore(0)`).
  Synthigy's login page allows 8, so a user who took 5–8 minutes logged in
  there and then failed the callback with `LOGIN_STATE_UNKNOWN`.
- **`LoginComplete` and `LoginCancel` refuse a login older than 10 minutes
  themselves.** Expiry used to live only in the store, so a custom store that
  never expired accepted a login of any age.
- **Permission denials are `iam`, not retryable `internal`.**
  `RELATION_FORBIDDEN`, `ATTRIBUTE_FORBIDDEN`, `ROW_FORBIDDEN`,
  `CREATE_FORBIDDEN`, `DELETE_FORBIDDEN` and `SLOT_OCCUPIED` were missing from
  the code table, so a denied relation read came back as a retryable server
  error.
- **A code the SDK doesn't know is no longer retryable.** It stays category
  `internal`, but `Retryable` is true only when the server marks the error
  retryable (it does for transient database failures) or the code is a known
  `network`/`rate_limit`/`internal` one.
- **A refused token mint says why.** `invalid_client` → `INVALID_CLIENT` (with
  a `Hint` to check the client id and secret), `invalid_scope` →
  `INVALID_SCOPE`, `invalid_target` → `INVALID_AUDIENCE`; other refusals stay
  `UNAUTHORIZED`. `Message` carries the server's `error_description`,
  `Details` the decoded OAuth error, `Status` the HTTP status (it was unset). A
  wrong secret used to read the same as an expired session.

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
