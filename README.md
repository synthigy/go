# Synthigy Go SDK

A zero-dependency Go client for Synthigy's `/data` endpoint. Native port of
the [JavaScript SDK](https://github.com/synthigy/js) covering OAuth
client-credentials auth, the full CRUD operation set, XSQL queries, SQL
templates, schema introspection, the temporal `/history` API, record
subscriptions, and SSE-based live streaming.

```bash
go get github.com/synthigy/go
```

Standard library only — no external modules.

The module path ends in `/go` but the package is named `synthigy`, so import
it under that name explicitly — `goimports` will otherwise guess `go`.

## Quick start

```go
import (
    "context"
    synthigy "github.com/synthigy/go"
)

type Movie struct {
    Xid    string `json:"xid"`
    Title  string `json:"title"`
    Genres []struct {
        Name string `json:"name"`
    } `json:"genres"`
}

// Endpoint and identity come from `synthigy exec`. Connect installs a
// process-wide default; the package-level functions all run on it.
if err := synthigy.Connect(synthigy.Config{}); err != nil {
    log.Fatal(err)
}
defer synthigy.Disconnect()

ctx := context.Background()

// XSQL: the shape you write is the shape you get back
movies, err := synthigy.QueryAs[Movie](ctx, `
movie (release_year > ?since:int, limit 10)
  xid
  title
  ->genres
    name`, map[string]any{"since": 1990},
    synthigy.ActingAs(sessionUserXID), // per-call impersonation
)
```

Run it with `synthigy exec -- go run .`

Identity is multiplexed per call with `ActingAs` (the BFF pattern), never a
second `Connect`; calling `Connect` again destroys the previous client (its
live watches close) before installing the new one. For tests or a rare second
endpoint, construct an explicit `*synthigy.Client` with `New` and call its
methods directly — the escape hatch.

## Code generation

Write your queries in `.xsql` files and get typed functions for them. The
server compiles the queries, so the types always match what it returns.

**1. Get a server.** In your project folder:

```bash
synthigy env init
synthigy up
```

The first `up` prints a `/setup` link; open it and pick a database. (No
browser? `synthigy up --db sqlite` skips the wizard.) Already have a server?
Skip this step.

**2. Deploy your data model** in the modeler (or from code with `synthigy.Deploy()`, as a
client with the Dataset Developer role).

**3. Connect as your app.** Create its client once, then save it to the
project:

```bash
synthigy iam add-client "My App" --id my-app --type confidential \
  --role "Dataset Explorer" --api Synthigy --grant client_credentials --local
synthigy connect http://localhost:7887 --client-id my-app
```

`add-client` prints the secret once; `connect` asks for it. Code is generated
for what this app is allowed to see. (`--local` works on the server's own
machine; for a remote server, create the client in the console.)

**4. Install the SDK:**

```bash
go get github.com/synthigy/go
```

**5. Write a query** in `xsql/movies.xsql`:

```
@search list
movie (release_year > ?since:int=1990, limit ?limit:int=20)
  xid
  title
  release_year
```

**6. Generate:**

Add one line to any `.go` file in your module, e.g. `generate.go`:

```go
//go:generate go run github.com/synthigy/go/cmd/synthigy-gen
```

then:

```bash
synthigy exec -- go generate ./...
```

This writes `gen/generated.go` (package `gen`), and saves `xsql/schema.json` and `xsql/ops.ir.json`
next to your queries.

**7. Use it:**

```go
ctx := context.Background()
if err := synthigy.Connect(synthigy.Config{}); err != nil {
	panic(err)
}
since := int64(2000)
movies, err := gen.API.Movie.List(ctx, gen.MovieListParams{Since: &since})
```

```bash
synthigy exec -- go run .
```

`synthigy exec` gives your program the server address and the app's identity.
Without it, pass them yourself: `synthigy.Config{Endpoint: ..., ClientID: ..., ClientSecret: ...}`.

**After you edit a query**, run step 6 again. As long as the `.xsql` files are
unchanged it works offline from `xsql/ops.ir.json`; after an edit it needs the
server, and it never generates from outdated results. In CI:

```bash
go run github.com/synthigy/go/cmd/synthigy-gen -check
```

**What to commit:** your `.xsql` files and `xsql/ops.ir.json`.
`xsql/schema.json` is your whole data model, so commit it only in a private
repo.

`@watch` ops also get a `Watch<Name>()` method, `@batch` ops become `gen.API.<Batch>(ctx, params)` — one request, with a result and an `Err` field per member — and every entity gets typed `Sync` / `Stack` / `Delete`. Flags: `-xsql DIR` (default `./xsql`), `-out DIR` (default `./gen`), `-package NAME`.

## Auth

- **Client credentials**: set `ClientID` + `ClientSecret`. Tokens are fetched
  from `/oauth/token`, cached per audience, refreshed before expiry, and
  re-fetched automatically once on a `401`.
- **`Audience`** (or `$SYNTHIGY_AUDIENCE`): binds one audience to every mint
  this client makes. The platform's audience model is **opt-in by design** — a
  token minted naming no audience resolves to an identity-only audience that
  `/data` rejects, so without this every data call 401s. Set it to the server's
  `/data` audience, published at `/.well-known/synthigy` as
  `auth.oidc.audience`. Left unset the SDK names no audience, so an unentitled
  client keeps a soft `401` rather than a hard `invalid_target`.
- **Static token**: set `Token`. For an unauthenticated dev server, set
  `Token: ""` together with `StaticToken: true`.
- With none of the above in `Config`, `New` also falls back to — when
  `SYNTHIGY_SUPERVISED=1` — asking a supervising parent (`synthigy
  exec`/`agent`, or a robotics commander) for a token over the process's
  own stdio, then the `SYNTHIGY_TOKEN` env var. The pipe deliberately beats the env
  var: `exec` injects the cached token *and* supervises, and only the pipe
  can refresh mid-run. With no source at all, `New` returns a
  `*synthigy.Error{Code: "NO_TOKEN"}` whose message teaches the fix.
- `synthigy.Token(ctx, synthigy.Audience("robotics"))` mints tokens for external
  services that trust Synthigy as their IdP.

### Logging users in (`LoginStart` / `LoginComplete`)

Authorization code + PKCE for a confidential server (BFF): the SDK owns the
protocol, your app owns sessions, cookies and routing. `LoginStart` returns a
URL, `LoginComplete` takes the callback's `code`/`state`; the handlers are
yours. The in-flight login lives in `Config.LoginStore` — a `LoginStore`
(`Put`/`Take`, `Take` one-shot) backed by whatever holds your sessions.

```go
synthigy.Connect(synthigy.Config{
    ClientID: "my-bff", ClientSecret: os.Getenv("BFF_SECRET"),
    LoginStore: synthigy.NewMemoryLoginStore(0),
})

http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
    u, err := synthigy.LoginStart(r.Context(), synthigy.LoginStartOptions{
        RedirectURI: callbackURL, ReturnTo: r.URL.Query().Get("returnTo")})
    if err != nil { http.Error(w, err.Error(), 500); return }
    http.Redirect(w, r, u, http.StatusFound)
})

http.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
    q := r.URL.Query()
    if q.Get("error") != "" { // the user cancelled at the IdP
        back, ok, _ := synthigy.LoginCancel(r.Context(), q.Get("state"))
        if !ok { back = "/" }
        http.Redirect(w, r, back, http.StatusFound)
        return
    }
    res, err := synthigy.LoginComplete(r.Context(), q.Get("code"), q.Get("state"), callbackURL)
    if err != nil { http.Error(w, err.Error(), 400); return }
    // res.User = {XID, Name, Scopes}; your session, your cookie
    http.Redirect(w, r, res.ReturnTo, http.StatusFound)
})
```

- `res.User.XID` is read from the id_token — no `/data` lookup. Pass it as
  `ActingAs` to act on the user's behalf.
- `MemoryLoginStore` is **single process**: behind a load balancer the
  callback can land on an instance that never saw `/login`
  (`LOGIN_STATE_UNKNOWN`).
- No store → `NO_LOGIN_STORE`; no `ClientSecret` →
  `LOGIN_REQUIRES_CONFIDENTIAL_CLIENT`. Other codes: `LOGIN_NONCE_MISMATCH`,
  `LOGIN_EXCHANGE_FAILED` (with `Status`).
- `PublicEndpoint` when the browser reaches the IdP on a different URL than
  this process does (containers, reverse proxies).
- The code exchange never retries: an authorization code is one-shot.

## Operations

All network methods take a `context.Context` first and return `(result, error)`.
Errors are `*synthigy.Error` (use `errors.As`); inspect `.Code`, `.Category`
(`auth`/`iam`/`validation`/`not_found`/`conflict`/`rate_limit`/`network`/`internal`),
and `.Retryable`.

| Method | Purpose |
|---|---|
| `QueryAs[T]` / `QueryRecords` / `QueryRecord` | XSQL reads with `?name:type` params, incl. `_count` / `_agg` rollups |
| `SQLTemplate` | ERD-aware SQL with `{entity.field}` placeholders (totals, joins, windows) |
| `Sync` / `Stack` / `Slice` / `Delete` / `Purge` | writes (see below) |
| `Schema` / `Lint` / `DeployedModel` / `RuntimeModel` | introspection |
| `History()` | temporal `get-at` / `events` / `diff` / `timeline` / `since` |
| `Exec` | several operations in one request (`OpQuery`, `OpStack`, …) |

**Writes are silent by default** — `Sync`/`Stack` answer `{"count": n}`, not
the record. Mint the id up front when you need it; that is cheaper than the
echo and makes a retried write idempotent rather than duplicating a row:

```go
xid := synthigy.NewXID()                                  // 22-char Base58
c.Sync(ctx, "movie", map[string]any{"xid": xid, "title": "Dune"})
// -> synthigy.Record{"count": 1}

c.Sync(ctx, "movie", map[string]any{"xid": xid, "title": "Dune"},
    synthigy.Returning())                                 // -> the written record
```
### Aggregation

`_count` and `_agg` compute over a record's relations in the database — no
child rows cross the wire:

```go
rows, _ := synthigy.QueryRecords(ctx, `
movie (limit 10)
  title
  _count
    actors:actors
  _agg
    ratings:movie_ratings
      value: avg`, nil)
// rows[i]["_count"] == map[string]any{"actors": 11}
// rows[i]["_agg"]   == map[string]any{"ratings": map[string]any{"value": map[string]any{"avg": 4.3}}}
```

For anything that is **not** a relation rollup — totals across an entity,
cross-entity joins, window functions — use `SQLTemplate`:

```go
rows, _ := synthigy.SQLTemplate(ctx, "SELECT count(*) AS n FROM {movie}", nil)
// rows[0]["n"] == 9742
```

## Filters, sorting, relations

All of it is written in the query:

```go
rows, _ := synthigy.QueryRecords(ctx, `
movie (release_year >= ?from:int, order by title asc, limit 20)
  title (ilike ?q:string="%")
  ->genres
    name`, map[string]any{"from": 2000, "q": "%dune%"})
```

- `?name:type=default` are named params, passed as a map.
- `->genres` is a **left** pull — a movie with no genres is still returned.
  `-genres` is **inner**: only movies that have a genre come back.
- A filter on a field (`title (ilike ?q)`) filters the rows.

## Live streaming (channels)

```go
// Notify-then-refetch: observe specific records.
stream, _ := synthigy.Observe(ctx, synthigy.RecordsDesc(userXID))
defer stream.Close()
for ev := range stream.Events() {
    // ev.Type e.g. "record/update"; refetch with QueryAs
}

// Live analytics tile.
tile, _ := synthigy.WatchSqlTemplate(ctx,
    "SELECT count(*) AS n FROM {user_rating}", nil,
    synthigy.Entities("user-rating"))
defer tile.Close()
log.Println(tile.First())          // initial value
for ev := range tile.Events() {     // ev.Type == "result/changed"
    log.Println(tile.First())
}

// Live result-set with diffing.
qw, _ := synthigy.WatchQueryXSQL(ctx, "movie (limit 5, order by release_year desc)\n  title", nil,
    synthigy.Entity("movie"))
defer qw.Close()
for ev := range qw.Events() {        // query/added | query/changed | query/removed
    _ = ev
}
```

`Listen`, `Watch`, and `WatchSchema` give lower-level access to the shared SSE
multiplexer. Every stream is a `*Stream[T]`: range `Events()`, call `Close()`
to stop, and check `Err()` after the channel closes.

## Testing

```bash
go test ./...                      # unit tests (no server)
go test -tags integration ./...    # against a live server (env-gated)
```

Set `SYNTHIGY_ENDPOINT` plus `SYNTHIGY_CLIENT_ID`/`SYNTHIGY_CLIENT_SECRET` (or
`SYNTHIGY_TOKEN`) for integration tests, and
`SYNTHIGY_TEST_LOGIN_CLIENT_ID`/`_SECRET`/`_USER`/`_PASSWORD` for the headless
browser-login test. Runnable services live in
[synthigy/examples](https://github.com/synthigy/examples): `movies/go` for the
smallest shape, `rls-demo/go` for the access-control BFF.
```

## License

MIT — see [LICENSE](LICENSE). The SDKs are permissive client libraries; the
Synthigy engine is fair-code under the Sustainable Use License.
