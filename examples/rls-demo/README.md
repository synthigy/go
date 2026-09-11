# RLS Demo

Synthigy port of the EYWA Row-Level Security demo (`git@github.com:neyho/demo.git`).

**One database. Four users. Four different views of the same data.**

## Status

| Piece | Status |
|-------|--------|
| `Project Management` versions 0.1.0 / 0.1.1 / 0.1.2 | ✅ converted |
| `Resource Planning` version 0.1.0 | ✅ converted |
| Seed data (users / groups / projects / tasks / allocations) | ✅ JSON fixtures, seeded over `/data` — no REPL |
| Per-user scripts (alice / bob / charlie / diana) | ✅ acted via `acting_as` per panel |

## Layout

```
sdk/go/examples/rls-demo/
├── bff/                         ← Go + Datastar + Tyrell BFF
│   ├── generate.go             ←   //go:generate directive (runs the generator)
│   ├── synthigy/               ←   codegen contract (committed)
│   │   ├── project.xsql        ←     the PM queries as XSQL ops
│   │   ├── schema.json         ←     pulled model snapshot (the "lockfile")
│   │   └── ops.ir.json         ←     compiled op IR
│   └── gen/                     ←   typed API generated from the contract (generated.go is git-ignored)
├── datasets/                    ← deployable models
│   ├── project-management@0.1.0.json   ← bare schema, NO RLS, NO User stub
│   ├── project-management@0.1.1.json   ← + User stub, + owner/members/assignee, RLS disabled
│   ├── project-management@0.1.2.json   ← RLS enabled (5 guards across Project + ProjectTask)
│   ├── resource-planning@0.1.0.json    ← Allocation + ProjectTask stub, cross-dataset RLS
│   └── seed/                    ← demo data as JSON fixtures (committed, seeded over /data)
│       ├── role.json  users.json  group.json
│       └── projects.json  tasks.json  allocations.json
└── bench/                       ← load-test datasets (pm-100k)
```

### Typed data access (codegen)

The BFF reads via a typed API generated from `bff/synthigy/project.xsql`. Row
types (`gen.ProjectList`, `gen.ProjectTaskList`) and methods (`api.Project.List`,
`api.Project.TaskCount`, …) come from the Go generator; `acting_as` still rides
on every call, so RLS is unchanged.

The whole loop is native Go — **no Node**. From the `bff/` module:

```
# regenerate offline from the committed contract (no server):
go generate ./...

# refresh the contract from a running server, then regenerate:
SYNTHIGY_CLIENT_ID=rls-demo-bff SYNTHIGY_CLIENT_SECRET=… \
  go run github.com/synthigy/go/cmd/synthigy-gen -pull
```

Commit the contract (`bff/synthigy/{project.xsql,schema.json,ops.ir.json}`);
`bff/gen/generated.go` is a build artifact (git-ignored). Defaults (`-xsql
./synthigy`, `-out ./gen`) mean the `//go:generate` directive in
`bff/generate.go` is just `synthigy-gen` — no paths to get wrong.

### Self-contained seeding (no REPL)

The demo owns its data. On startup the BFF checks whether the roster's first
user resolves; if not, it seeds the committed fixtures in `datasets/seed/` over
`/data` via the SDK (`sync`, upsert by xid), in dependency order (role → users →
group → projects → tasks → allocations). `POST /reseed` re-runs it to reset to
the pristine state. **No Clojure REPL, no `clj-nrepl-eval`, no Node** — a fresh
checkout is `go run .` and it self-seeds. (The RLS on/off toggle likewise
deploys via the `/data` deploy op, not a REPL.)

## Version progression — what each one unlocks

Same `Project` and `Project Task` xids (`7Skf8MvReE4XsEQXdtHXWn`,
`MiBTZ5y2TYyBSXhyXiXBxK`) across all three Project Management versions, so they
upgrade in place.

### `project-management@0.1.0` — bare schema

| Entity | Attributes | RLS |
|--------|-----------|-----|
| Project | Active, Name, Description, Avatar | — |
| Project Task | Title, Description, Status, Due Date | — |

Relations: `project ↔ tasks` (o2m).

**No User stub, no Assignee, no project_owner, RLS disabled.** Deploy this alone
and *everyone* sees *everything*. Useful baseline to show what "no security"
looks like in Synthigy — and to demonstrate that Resource Planning's
cross-dataset RLS guard fails open until ProjectTask grows the `assignee`
attribute it references.

### `project-management@0.1.1` — structure but unsecured

| Entity | Attributes | RLS |
|--------|-----------|-----|
| Project | (same) | — |
| Project Task | + Assignee (user), Assignee Group (group), Priority | — |
| **User** (stub) | Type, Password, Name, Priority, Active, Avatar, Settings | — |

Relations: + `project owner ↔ owner` (o2o), + `members ↔ projects` (o2m).

**The model is fully shaped but `rls.enabled = false`** — guards are not
declared yet. This is the "structure is right, security is off" state. Good
for demoing what the data looks like before you turn on protection, and for
catching code that assumes RLS will save it.

### `project-management@0.1.2` — RLS flipped on

| Entity | Guards | Operation |
|--------|--------|-----------|
| Project | `project_owner → User` (relation) | read + write + delete |
| Project | `members → User` (relation, m2m) | read |
| Project Task | `assignee` (ref) | read |
| Project Task | `assignee_group` (ref) | read + write |
| Project Task | `project → project_owner → User` (relation, 2-hop) | read + write + delete |

Same entities/relations as 0.1.1. Just `rls.enabled = true` and the guard
list populated. **Upgrading from 0.1.1 → 0.1.2 demonstrates a "security
turn-on" deploy** — same data, suddenly Diana sees nothing.

### `resource-planning@0.1.0`

| Entity | Attributes | RLS |
|--------|-----------|-----|
| Allocation | Hours, Start Date, End Date, Notes, User (user-type) | 2 guards |
| **Project Task** (stub — same xid as in PM) | Status, Description, Priority, ... | empty `rls.enabled = true` |

Relations: `Project Task ↔ allocations` (o2m, cross-dataset).

Allocation guards:
- `:ref` on `Allocation.user` → read + write your own allocation
- `:hybrid` on `Allocation → ProjectTask.assignee` → read allocations on tasks
  you're assigned to

The ProjectTask stub here intentionally carries `rls.enabled = true` but no
guards — when Resource Planning is deployed *without* Project Management 0.1.2,
no protection kicks in via the stub side; the structural guards live on the
real ProjectTask in `project-management@0.1.2`.

## Deploy progressions worth trying

```clojure
;; Helper
(require '[synthigy.transit :as transit] '[synthigy.dataset :as dataset])
(defn load-v [path] (transit/<-transit (slurp path)))

;; A — Bare model, no security, no assignment concepts
(dataset/deploy! synthigy.db/*db* (load-v ".../project-management@0.1.0.json"))
(dataset/deploy! synthigy.db/*db* (load-v ".../resource-planning@0.1.0.json"))
;; Resource Planning's :hybrid guard references ProjectTask.assignee — which
;; doesn't exist yet. Observe how Synthigy handles the dangling reference.

;; B — Structure complete, RLS off
(dataset/deploy! synthigy.db/*db* (load-v ".../project-management@0.1.1.json"))
;; Schema picks up assignee/group/owner. Allocation's :hybrid guard now
;; resolves to a real attribute, but Project Management RLS is disabled.

;; C — Flip RLS on
(dataset/deploy! synthigy.db/*db* (load-v ".../project-management@0.1.2.json"))
;; Existing data is unchanged but visibility narrows. The "Diana sees
;; user_task = nil" moment goes live here.

;; D — Skip the middle: 0.1.0 → 0.1.2 directly
;; (Fresh DB, deploy resource-planning, then deploy PM 0.1.2 without ever
;; seeing 0.1.1.) Tests that the merge handles big-step upgrades.
```

## Cross-dataset identity (verified)

| Entity | xid | euuid |
|--------|-----|-------|
| `Project Task` (in both datasets) | `MiBTZ5y2TYyBSXhyXiXBxK` | `a7b6b66e-b10f-410c-a4a7-365b792042f0` |
| `User` (PM stub == canonical IAM User) | `WN5xU8Do5pcdhTkxXvEYwt` | `edcab1db-ee6f-4744-bfea-447828893223` |

## How the conversion was done

In a running Synthigy nREPL (`clj -M:postgres:httpkit:dev`):

```clojure
(require '[synthigy.transit :as transit]
         '[synthigy.dataset.patch.model :as model]
         '[synthigy.dataset.id :as id])
(transit/init)  ;; if not already done by lifecycle

(defn convert-eywa-file [in-path out-path]
  (let [v  (transit/<-transit (slurp in-path))        ;; EYWA tags → Synthigy records
        v' (update v :model #(model/transform-model % :euuid :xid))  ;; populate xids
        normalize-active (fn [coll]
                           (into {} (for [[k m] coll]
                                      [k (if (nil? (:active m)) (assoc m :active true) m)])))
        v' (-> v'
               (update-in [:model :entities] normalize-active)
               (update-in [:model :relations] normalize-active))
        with-xid (fn [m] (assoc m :xid (or (:xid m) (id/uuid->nanoid (:euuid m)))))]
    (spit out-path (transit/->transit (-> v' with-xid (update :dataset with-xid))))))
```

EYWA originals at `/tmp/neyho-demo/resources/*.json` if you want to re-convert.

## Next session — bringing the demo to life

1. **Deploy datasets** — try the four progressions above.
2. **Seed users** — `synthigy.iam/add-user` with stable EUUIDs from
   `/tmp/neyho-demo/src/demo/data.clj` (alice/bob/charlie/diana, all `password`).
   Engineering group via `synthigy.iam/add-group`.
3. **Seed projects / tasks / allocations** — Use the SDK or `dataset/sync-entity`.
4. **Per-user scripts** — Port `alice.clj` / `bob.clj` / `charlie.clj` / `diana.clj`
   from `/tmp/neyho-demo/src/demo/` to use the Clojure SDK against `/data`.
