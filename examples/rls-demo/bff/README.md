# RLS Demo BFF — Go + Datastar + Tyrell

A live row-level-security showcase. One Synthigy database, four users, **one
query rendered four ways** — so RLS is visible at a glance. A single Go BFF
holds one confidential, trusted OAuth token and switches identity per panel via
the `/data` `acting_as` trusted parameter (no per-user login).

![RLS on](../../../rls-demo.png)

## What it shows

- **4-panel grid** (Alice / Bob / Charlie / Diana). Each panel runs the same
  `search` + `sql-template` under a different `acting_as`, so the server applies
  that principal's RLS. Alice sees Project Alpha + its tasks; Bob sees Beta;
  Charlie sees only the tasks assigned to him (across both projects); Diana sees
  nothing.
- **RLS-aware analytics.** The cross-cutting bar is the identical
  `SELECT count(*) FROM {project_task}` per principal — different answers because
  `sql-template` runs under the `acting_as` RLS scope. (Most BaaS leak analytics
  via a service role; here even raw SQL respects RLS.) Note the braces: guards
  attach to `{…}` placeholders, so a bare `FROM project_task` is rejected rather
  than run unguarded. Aggregating across several entities means one CTE per
  entity — a scalar subquery puts the entity in a scope the guard can't reach.
  Entity- and relation-level RBAC apply too, exactly as they do to `search`.
  What raw SQL does *not* get is attribute-level filtering: aliases are
  deterministic, so the projection can name any column of an entity the
  principal can read. Rows are RLS-filtered; columns are not.
- **Live RLS on/off toggle.** "Toggle RLS" redeploys the Project Management
  model with the RLS config stripped (off) or restored (on) and re-seeds the
  deterministic cast — watch the panels diverge and converge in real time.
- **Live writes.** "Cycle a task" advances a task's status; the change shows up
  instantly in exactly the panels whose principal can see that task. Raw `psql`
  writes propagate too (the BFF holds an upstream plug watch and
  re-fetches — notify-then-refetch, never trusting delta payloads for RLS-scoped
  lists).

## Stack

- **Synthigy Go SDK** (`sdk/go`) — `/data` reads (`acting_as`, LEFT-join default,
  `sql-template`) + SSE watch.
- **Datastar Go SDK** (`github.com/starfederation/datastar-go`) — BFF→browser
  SSE; the BFF emits HTML, Datastar morphs the DOM.
- **Tyrell** (`tyrell-components@tc`, via CDN) — server-rendered `<ty-*>` web
  components (tags, icons, buttons, surfaces), dark theme. No build step.

## Prerequisites

1. A running Synthigy server on `:7887` (PostgreSQL `demo` DB).
2. The demo plug deployed + seeded, and a trusted confidential BFF client.
   From the backend dev nREPL:

   ```clojure
   (require '[synthigy.rls-demo.setup :as setup] '[synthigy.iam :as iam]
            '[synthigy.data :as data] '[synthigy.oauth.core :as oc])
   (setup/deploy-and-seed! {:pm-version "0.1.2"})         ; deploy + seed cast
   (iam/add-client {:id "rls-demo-bff" :name "RLS Demo BFF"
                    :secret "rlsbff-2Wm8Qz5Tn7Vx1Kp4Lr9Cs6Bd3Hf0Ga"
                    :type :confidential
                    :settings {"allowed-grants" ["client_credentials" "refresh_token"]
                               "redirections" ["http://localhost:8090/auth/callback"]
                               "trusted" true}})
   (iam/set-user {:name "rls-demo-bff" :type :SERVICE :active true
                  :roles [data/*ROOT*]})                  ; so acting_as is allowed
   (oc/get-client "rls-demo-bff")
   ```

   The four user xids are derived deterministically from fixed euuids
   (`id/uuid->nanoid`), so they're stable across reseeds — see `main.go`'s
   `roster`.

## Run

```bash
cd examples/rls-demo/bff
SYNTHIGY_NREPL_PORT=<backend nREPL port> go run .
# open http://localhost:8090
```

Config (env, with sensible local defaults):

| var | default |
|---|---|
| `SYNTHIGY_ENDPOINT` | `http://localhost:7887` |
| `SYNTHIGY_CLIENT_ID` | `rls-demo-bff` |
| `SYNTHIGY_CLIENT_SECRET` | the dev secret above |
| `PORT` | `8090` |
| `SYNTHIGY_NREPL_PORT` | (falls back to `core/.nrepl-port`) — needed for the RLS toggle / reseed, which drive the backend's `synthigy.rls-demo.setup`. |

`GET /?snapshot` renders the full grid server-side (no SSE) — handy for
screenshots and no-JS clients.

## Notes / known shape

- The live RLS toggle drives a backend redeploy via the dev nREPL — it's a local
  showcase, not a self-contained binary. The 4-panel read/analytics path is pure
  Go SDK and needs only the HTTP endpoint.
- RLS off lists *all* `project` rows in the `demo` DB (hundreds, from other
  datasets) — that's the point (full exposure); the list is capped with "+N more"
  while the tile shows the true SQL count.
