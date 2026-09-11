// Package synthigy is a zero-dependency Go client for Synthigy's /data
// endpoint.
//
// It is a native port of the JavaScript SDK (@synthigy/sdk) and covers the
// same surface: OAuth client-credentials auth, the full CRUD operation set
// (search/get/sync/stack/slice/delete/purge/count), XSQL queries,
// SQL templates, schema introspection, the temporal /history API, record
// subscriptions, and SSE-based live streaming (Listen/Observe and the
// Watch family).
//
// Single-client model: one process, one client, one backend. Connect installs
// a process-wide default client and the package-level verbs operate on it, so
// you never thread a *Client. Identity is multiplexed per call with ActingAs
// (the BFF pattern), never a second Connect.
//
//	if err := synthigy.Connect(synthigy.Config{
//	    Endpoint:     "https://synthigy.example.com",
//	    ClientID:     "my-service",
//	    ClientSecret: os.Getenv("SYNTHIGY_SECRET"),
//	}); err != nil {
//	    log.Fatal(err)
//	}
//	defer synthigy.Disconnect()
//
//	users, err := synthigy.Search(ctx, "user",
//	    synthigy.Args{"active": synthigy.Eq(true), "_limit": 10},
//	    synthigy.Selection{"name": nil, "email": nil,
//	        "roles": synthigy.Rel(synthigy.Selection{"name": nil})},
//	    synthigy.ActingAs(sessionUserXID))
//
// Calling Connect again destroys the previous client (its live watches close,
// its SSE session drops) before installing the new one — the Clojure
// server-restart idiom.
//
// Typed reads use the generic helpers, which also operate on the default:
// synthigy.SearchAs[T](ctx, "user", args, sel).
//
// Escape hatch: construct an explicit *Client with New and call its methods
// directly (for tests or a rare second endpoint) — the analog of Clojure's
// create-client + binding. Go has no goroutine-local binding, so parallel
// tests that need isolation use an explicit *Client rather than the global.
// The *Client escape hatch returns untyped Records; decode with DecodeRecord.
//
// Every network verb takes a context.Context for cancellation and deadlines.
// Errors are returned as values; transport and server errors are
// *synthigy.Error, inspectable with errors.As. Using a verb before Connect
// returns (or, for the few verbs without an error return, panics with) a
// NOT_CONNECTED *synthigy.Error.
package synthigy

// Version is the SDK version. Kept in step with the JS SDK package version.
const Version = "0.1.0"

// Record is a single entity record as returned by the /data endpoint —
// a dynamic attribute map keyed by attribute name (in the requested key
// format). Use the package-level generic helpers (Search[T], Get[T], …) or
// DecodeRecord to map records into typed structs.
type Record = map[string]any

// New constructs a Client from the given Config. It is the primary entry
// point. createClient is provided as an alias for familiarity with the JS
// SDK's createClient factory.
func New(cfg Config) (*Client, error) { return newClient(cfg) }

// CreateClient is an alias for New, mirroring the JS SDK's createClient.
func CreateClient(cfg Config) (*Client, error) { return newClient(cfg) }
