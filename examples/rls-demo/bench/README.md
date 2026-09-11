# RLS bench — write + read path

Measure the wall-clock cost of `:write` RLS guard injection on
`sync-entity`. Forks the shape of `examples/movies/bench/` but
targets the Project Management + Resource Planning models from
`examples/rls-demo/datasets/` — same engine, same harness shape,
the only thing that changes is the guard surface.

## What gets measured

Two model variants of the same ERD ship in `examples/rls-demo/datasets/`:

| Version | RLS | What the bench uses it for |
|---------|-----|----------------------------|
| `project-management@0.1.1` | off | baseline — engine cost with the same entities/relations but no guard machinery |
| `project-management@0.1.2` | on  | 5 guards across Project + Project Task (`:ref`, 1-hop and 2-hop `:relation`) |

The bench runs the same import (~1,000 users, ~50 projects, ~100k tasks,
~50k allocations) against each model. Order of phases:

1. **Setup** — deploy + seed users/groups/projects under `:system`.
2. **Insert pass (`:system` only)** — populate tasks and allocations.
   No rows pre-exist, so no `ON CONFLICT` fires and there's nothing
   for the new WHERE to evaluate. Establishes a fresh-row throughput
   baseline.
3. **Update pass (per mode)** — re-sync every row with a flipped
   `:status`. Every row now hits `ON CONFLICT DO UPDATE` and the
   `WHERE <rls>` predicate is evaluated per row. **This is where
   RLS overhead actually lands** — the comparison across modes
   below isolates the cost.

Three principal modes for the update pass:

| Mode | Principal | RLS injection runs? |
|------|-----------|---------------------|
| `:system` | `data/*SYNTHIGY*` (superuser) | no — `should-apply-guards?` short-circuits |
| `:none`   | no principal bound              | no — same short-circuit |
| `:user`   | a bound bench principal         | yes — WHERE injected, evaluated per row |

`:system` vs `:none` should be statistical noise (same code path).
`:user` vs `:system` = the cost of guard evaluation.

### Data shape — calibrated so every guard *passes*

The bench measures the **cost** of WHERE evaluation, not its filtering
behavior. To get a clean number, the generator pins:

- every project's `project_owner` to the bench principal (first user),
- every allocation's `user` to the same principal.

That way every `:write` guard evaluates to true under `:user` mode and
no rows get silently skipped. The cost we measure is "WHERE compiled,
bound, evaluated, returns true" per row, which is the relevant number
for capacity planning. The rls-demo tests already cover mixed
pass/fail correctness ([[rls_demo_test.clj]]).

## Layout

```
examples/rls-demo/bench/
├── README.md                ← this file
├── gen-data.clj             ← deterministic JSON-shard generator
├── synthigy_import.clj      ← bench harness (REPL-driven)
└── datasets/                ← generated JSON shards (gitignored)
    └── pm-100k/             ← default profile (~100k tasks)
        ├── users.json
        ├── groups.json
        ├── projects.json
        ├── tasks_0001.json
        ├── …
        └── allocations_0001.json
```

## How to run

### 1 — Generate the dataset (one-time, deterministic)

From a REPL booted with `:postgres:httpkit:dev` (or any dev REPL with
`synthigy.dataset` on the classpath):

```clojure
(load-file "examples/rls-demo/bench/gen-data.clj")
(rls-demo-bench.gen/generate!
  {:profile :pm-100k       ;; default; overrides go in profiles map below
   :seed 42})              ;; deterministic
```

Profiles (see `gen-data.clj`):

| Profile      | Users | Groups | Projects | Tasks  | Allocations |
|--------------|------:|-------:|---------:|-------:|------------:|
| `:pm-smoke`  |    50 |      5 |        5 |  1,000 |         500 |
| `:pm-100k`   | 1,000 |     50 |       50 |100,000 |      50,000 |

### 2 — Boot a fresh REPL (PG dev DB)

The bench harness deploys + destroys datasets; use a throwaway DB.

```bash
PGPASSWORD=bench psql -h localhost -p 5433 -U bench -d postgres \
  -c "DROP DATABASE IF EXISTS synthigy_rls_bench" \
  -c "CREATE DATABASE synthigy_rls_bench"

POSTGRES_HOST=localhost POSTGRES_PORT=5433 \
POSTGRES_USER=bench POSTGRES_PASSWORD=bench POSTGRES_ADMIN_DB=postgres \
POSTGRES_DB=synthigy_rls_bench SYNTHIGY_LOG_SINKS= SYNTHIGY_ID_FORMAT=xid \
clj -M:postgres:httpkit:dev
```

### 3 — Run the bench

The harness needs the IAM system initialized. Easiest path: call the
test-helper's `initialize-system!` first (same lifecycle the test
fixture uses), then load + run the bench.

```clojure
(require 'synthigy.test-helper)
(synthigy.test-helper/initialize-system!)

(load-file "examples/rls-demo/bench/synthigy_import.clj")

;; sanity-check first (~30 seconds)
(rls-demo-bench.import/run!
  {:profile :pm-smoke :model "0.1.2" :batch 500
   :modes [:system :none :user]})

;; full bench — RLS-on model
(rls-demo-bench.import/run!
  {:profile :pm-100k :model "0.1.2" :batch 1000
   :modes [:system :none :user]})

;; baseline run — no-RLS model
(rls-demo-bench.import/run!
  {:profile :pm-100k :model "0.1.1" :batch 1000
   :modes [:system]})
```

Returns and pretty-prints a comparison table like:

```
=== SUMMARY ===
model=0.1.2  profile=pm-100k  batch=1000
pass     mode      tasks-rec/s  alloc-rec/s      wall-secs
insert   system          10,400        10,300          14.41
update   system           9,800         9,640          14.92
update   none             9,750         9,610          14.99
update   user             8,210         8,090          17.41   ← RLS overhead
```

`:system` vs `:user` here = the cost of `ON CONFLICT DO UPDATE WHERE
<rls-sql>` against the deployed guard mix (1× direct `:ref`, 1× 1-hop
relation, 1× 2-hop relation on Project Task; 1× direct `:ref` on
Allocation).

## Read-path bench

After the write bench has populated the DB (`run!` returned), exercise
the read APIs against the same data with `read-bench!`. Six read shapes
× three principal modes = 18 cells; reports both row counts (RLS
correctness) and median latency (RLS cost).

```clojure
(rls-demo-bench.import/read-bench! {:profile :pm-100k :reps 5})
```

### Modes

| Mode        | Bound principal                          | RLS shape                                                              |
|-------------|------------------------------------------|------------------------------------------------------------------------|
| `:system`   | `data/*SYNTHIGY*` superuser              | Bypassed (`should-apply-guards?` short-circuits). Control.             |
| `:owner`    | First user (bench principal, owns everything) | All `:read` guards evaluate to `true` per row — pure cost-of-evaluation. |
| `:assignee` | Second user from `users.json`            | Owns nothing. Sees only what `:ref assignee` / `:ref assignee_group` / cross-dataset `:hybrid` guards expose — true RLS filter. |

### Ops

| Op                                 | What it tests                                               |
|------------------------------------|-------------------------------------------------------------|
| `search-tasks-paginated`           | `dataset/search-entity` with `:_limit 100 :_order_by`       |
| `aggregate-task-count`             | `dataset/aggregate-entity` with `{:count nil}` selection    |
| `projects-with-task-count`         | `dataset/search-entity` with `:_count` selector on `:tasks` |
| `tasks-with-alloc-hours-sum`       | `dataset/search-entity` with `:_agg` over cross-dataset `:allocations` relation, summing `:hours` |
| `allocations-search`               | Single-relation entity with one `:ref user` write-guard — bench's "cheap RLS" reference point |
| `sql-template-done-tasks`          | `template/execute-template` raw SQL with entity placeholders — exercises `template/inject-rls` path |

### Reading the output

**Correctness table** — row counts per (op × mode). For the unfiltered
ops (no LIMIT), `:system` and `:owner` should match (Lucia sees
everything); `:assignee` should be much smaller (their RLS-filtered
slice). For LIMIT-100 ops the cap masks filtering but timing still
varies.

**Performance table** — median ms per (op × mode), with `:owner vs
:system` Δ% as the headline RLS-cost number.

### What the bench surfaced

While building the read-path, the SQL-template op uncovered a bug in
`core/core/src/synthigy/dataset/sql/template.clj/inject-rls`: it was
naively concatenating `" AND <rls>"` to the END of the rendered SQL,
producing `LIMIT 100 AND <rls-expr>` — PG rejects with `argument of
AND must be type boolean`. Same defect with `ORDER BY`, `GROUP BY`,
`HAVING`, `OFFSET`, `FETCH`. Fixed in the same session via a new
`split-at-tail-clauses` helper that injects RLS BEFORE the tail
clauses. `template-test` still green (82/212/0).

## Security demo — confirms RLS is doing what it claims

After `run!` + `read-bench!`, `security-demo!` runs three write attacks
as the `:assignee` principal (a user with no roles, in one group,
whose RLS scope sees ~597 of 100k tasks, 0 of 50 projects). Each
outcome is verified against the DB under `:system` afterwards.

```clojure
(rls-demo-bench.import/security-demo! {:profile :pm-100k})
```

Expected outcomes on `project-management@0.1.2`:

| Attack                              | Outcome                          | Layer responsible          |
|-------------------------------------|----------------------------------|----------------------------|
| UPDATE foreign existing task        | silent no-op                     | **RLS** — `ON CONFLICT DO UPDATE WHERE rls` returns false; PG skips the row; RETURNING returns nothing; no exception. Silent-skip is the right shape here because sync-entity is batch-native — one bad row in a chunk shouldn't sink the whole call. |
| DELETE foreign existing task        | **throws `DELETE_FORBIDDEN`**    | **RLS** — `delete-entity` is single-target; the caller named one specific row by xid/unique key. Silent "ok" when nothing happened would mislead them. After the RLS-filtered scope comes back empty, an unfiltered existence probe distinguishes "doesn't exist for anyone" (returns `true`, preserves nonexistent contract) from "exists but RLS hides it" (throws ex-info with `:code "DELETE_FORBIDDEN"`). |
| INSERT new task with foreign parent | succeeds                         | **RBAC** (off in this demo) — the PM/RP model ships RBAC-disabled on every entity so `entity-allows? :write` short-circuits to true. With RBAC turned on and the bench user lacking write perms, this call would be rejected before sync-entity touched SQL. |

**What this actually demonstrates:**

- RLS is doing its job — Lucia's existing rows are not modifiable or
  deletable by Yara. Her project, her tasks, and her allocations are
  intact.
- The third row is not an RLS gap; it's a demonstration that this
  demo intentionally has RBAC turned off so the focus stays on RLS
  semantics. Production deployments would set
  `:configuration/:rbac/:enabled true` on Project Task and grant
  `:write` only to roles that should create tasks.
- It's also a reminder that "task can have a project" doesn't mean
  "task must belong to a project that you own" — tasks may be
  personal todos, process steps, or intake items, so blanket
  "must-own-parent-to-create" semantics would be wrong for the
  general entity.

The attacker who runs Attack 3 successfully then can't read, update,
or delete the resulting row (all of Yara's RLS guards fail against
it). The row is data pollution in Lucia's project, not data
exfiltration — Lucia sees it, can clean it up, and Yara cannot
verify her own write landed.

## Why this exists

The 2026-06-02 sync-entity RLS fix injected `WHERE <rls-sql>` into
the `ON CONFLICT DO UPDATE` clause inside the shared
`store-entity-records` write path. The movies bench is great for raw
write throughput but has no RLS, so it can't tell us what that
WHERE costs in practice. This bench fills the gap on a model whose
guard mix is representative of real multi-tenant SaaS (mix of `:ref`
direct-column matches and 1-hop / 2-hop relation walks).
