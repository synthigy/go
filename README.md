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

// One process, one client, one backend. Connect installs a process-wide
// default; the package-level verbs operate on it, so you never thread a client.
if err := synthigy.Connect(synthigy.Config{
    Endpoint:     "https://synthigy.example.com",
    ClientID:     "my-service",
    ClientSecret: os.Getenv("SYNTHIGY_SECRET"),
}); err != nil {
    log.Fatal(err)
}
defer synthigy.Disconnect()

ctx := context.Background()

users, err := synthigy.Search(ctx, "user",
    synthigy.Args{"active": synthigy.Eq(true), "_limit": 10},
    synthigy.Selection{
        "name":  nil,
        "email": nil,
        "roles": synthigy.Rel(synthigy.Selection{"name": nil}),
    },
    synthigy.ActingAs(sessionUserXID), // per-call impersonation
)
```

Identity is multiplexed per call with `ActingAs` (the BFF pattern), never a
second `Connect`; calling `Connect` again destroys the previous client (its
live watches close) before installing the new one. For tests or a rare second
endpoint, construct an explicit `*synthigy.Client` with `New` and call its
methods directly — the escape hatch.

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
  own stdio, then the `SYNTHIGY_TOKEN` env var (see
  `docs/plans/PLAN-EXEC-IDENTITY.md`). The pipe deliberately beats the env
  var: `exec` injects the cached token *and* supervises, and only the pipe
  can refresh mid-run. With no source at all, `New` returns a
  `*synthigy.Error{Code: "NO_TOKEN"}` whose message teaches the fix.
- `synthigy.Token(ctx, synthigy.Audience("robotics"))` mints tokens for external
  services that trust Synthigy as their IdP.

## Operations

All network methods take a `context.Context` first and return `(result, error)`.
Errors are `*synthigy.Error` (use `errors.As`); inspect `.Code`, `.Category`
(`auth`/`iam`/`validation`/`not_found`/`conflict`/`rate_limit`/`network`/`internal`),
and `.Retryable`.

| Method | Purpose |
|---|---|
| `Search` / `Get` | reads (incl. `_count` / `_agg` relation rollups) |
| `Sync` / `Stack` / `Slice` / `Delete` / `Purge` | writes (see below) |
| `SQLTemplate` | ERD-aware SQL with `{entity.field}` placeholders (totals, joins, windows) |
| `Query` / `QueryRecords` / `QueryRecord` | XSQL selection-DSL with `?name:type` params |
| `SearchTree` / `GetTree` | self-FK tree reads (auto-composed) |
| `Schema` / `Lint` / `DeployedModel` / `RuntimeModel` | introspection |

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
| `History()` | temporal `get-at` / `events` / `diff` / `timeline` / `since` |
| `Exec` | raw batched operations (`Op*` builders) |

### Aggregation

There is **no standalone aggregate operation**. Aggregates over a record's
**relations** ride inline in a `Search`/`Get` selection via `_count` / `_agg`:

```go
// count related actors per movie
rows, _ := synthigy.Search(ctx, "movie", synthigy.Args{"_limit": 10},
    synthigy.Selection{
        "title":  nil,
        "_count": synthigy.Selection{"actors": nil},
    }, synthigy.KeyFormat("kebab"))
// rows[i]["_count"] == map[string]any{"actors": 11}

// aggregate a numeric field across a relation
synthigy.Search(ctx, "user-role", args, synthigy.Selection{
    "name": nil,
    "_agg": synthigy.Selection{
        "users": synthigy.Selection{
            "priority": synthigy.Selection{"sum": nil, "avg": nil},
        },
    },
})
// row["_agg"] == {"users": {"sum": {"priority": 60}}}
```

For anything that is **not** a relation rollup — totals across an entity,
cross-entity joins, window functions — you **must** use `SQLTemplate` (or XSQL
via `Query`):

```go
rows, _ := synthigy.SQLTemplate(ctx, "SELECT count(*) AS n FROM {user}", nil)
// rows[0]["n"] == 1702
```

Typed decoding via package-level generics:

```go
type User struct { Name string `json:"name"` }
us, err := synthigy.SearchAs[User](ctx, "user", nil, synthigy.Fields("name"))
```

## Filters & selections

```go
synthigy.Args{
    "active": synthigy.Eq(true),
    "_where": synthigy.Or(
        synthigy.Where{"role": synthigy.Eq("admin")},
        synthigy.Where{"age":  synthigy.Ge(18)},
    ),
    "_order_by": [][2]string{{"name", "asc"}},
    "_limit":    20,
}
```

Selections accept scalars (`nil`), nested relations (`Selection{...}`), and
`Rel(sel, WithArgs(...), WithAlias(...))`; `Fields("a","b")` is shorthand for a
flat scalar list.

### Relations LEFT-join by default

A relation you project is **LEFT-joined** so it never drops its parent —
this is the server's own default (absent `_join` = LEFT, for every
operation): a selection is a projection, and relation args filter the
*children*, never the parent set. The SDK injects nothing; the wire
carries exactly what you wrote.

To scope parents to those HAVING the relation (INNER), set `_join`
explicitly:

```go
synthigy.Search(ctx, "User", nil, synthigy.Selection{
    "name":  nil,
    "roles": synthigy.Rel(synthigy.Selection{"name": nil},
        synthigy.WithArgs(synthigy.Args{"_join": "inner"})),
})
```

`_count` / `_agg` rollups also LEFT-join.

## Live streaming (channels)

```go
// Notify-then-refetch: observe specific records.
stream, _ := synthigy.Observe(ctx, synthigy.RecordsDesc(userXID))
defer stream.Close()
for ev := range stream.Events() {
    // ev.Type e.g. "record/update"; refetch with Search/Get
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
qw, _ := synthigy.WatchQuery(ctx, "user", synthigy.Args{"active": synthigy.Eq(true)},
    synthigy.Fields("name"))
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
`SYNTHIGY_TOKEN`) for integration tests. Runnable services live in
[synthigy/examples](https://github.com/synthigy/examples): `movies/go` for the
smallest shape, `rls-demo/go` for the access-control BFF.
```

## License

MIT — see [LICENSE](LICENSE). The SDKs are permissive client libraries; the
Synthigy engine is fair-code under the Sustainable Use License.
