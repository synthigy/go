package synthigy

import (
	"context"
	"encoding/json"
	"sync"
)

// This file is the single-client layer — the Go analog of the Clojure SDK's
// process-wide *client* dynvar. Connect installs a package-global client and
// the package-level verbs below (Search, Sync, Watch, …) operate on it, so
// callers never thread a *Client. Identity is multiplexed per call with
// ActingAs, never a second Connect.
//
// The escape hatch is the *Client type itself: construct one with New and call
// its methods directly (tests, a rare second endpoint) — the analog of
// Clojure's create-client + `binding`. Go has no goroutine-local binding, so
// parallel tests that need isolation use an explicit *Client rather than the
// global.

var (
	defaultMu     sync.RWMutex
	defaultClient *Client
)

// Connect creates a client from cfg and installs it as the process-wide
// default — the Clojure server-restart idiom: any PREVIOUS default is
// destroyed first (its live watches close, its SSE session drops) before the
// new one replaces it. On a construction error the current default is left
// untouched. Call once at startup; call again to reconnect.
//
// One process, one client, one backend: multiplex identity per call with
// ActingAs, never a second Connect.
func Connect(cfg Config) error {
	c, err := newClient(cfg)
	if err != nil {
		return err
	}
	defaultMu.Lock()
	prev := defaultClient
	defaultClient = c
	defaultMu.Unlock()
	if prev != nil {
		prev.Close() // destroy the old one, server-restart style
	}
	return nil
}

// Disconnect destroys the current default client — closing every live watch and
// dropping the SSE session — and uninstalls it. No-op when not connected.
// Connect calls this on the previous client automatically.
func Disconnect() {
	defaultMu.Lock()
	prev := defaultClient
	defaultClient = nil
	defaultMu.Unlock()
	if prev != nil {
		prev.Close()
	}
}

// Default returns the process-wide client installed by Connect, or nil when not
// connected. The package-level verbs (including the typed generics like
// SearchAs[T]) already operate on it; Default is for introspection or handing
// the client to code that needs an explicit *Client.
func Default() *Client {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultClient
}

// dflt resolves the default client for error-returning verbs.
func dflt() (*Client, error) {
	defaultMu.RLock()
	c := defaultClient
	defaultMu.RUnlock()
	if c == nil {
		return nil, newError("not connected — call synthigy.Connect first", "NOT_CONNECTED")
	}
	return c, nil
}

// mustDflt resolves the default client for the few verbs whose signatures carry
// no error return; using the SDK before Connect is a programmer error and
// panics with NOT_CONNECTED (the analog of the Clojure client throwing).
func mustDflt() *Client {
	c, err := dflt()
	if err != nil {
		panic(err)
	}
	return c
}

// ---------------------------------------------------------------------------
// CRUD + XSQL
// ---------------------------------------------------------------------------

func Search(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) ([]Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Search(ctx, entity, args, sel, opts...)
}

func Get(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, entity, args, sel, opts...)
}

func Sync(ctx context.Context, entity string, data map[string]any, opts ...Opt) (Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Sync(ctx, entity, data, opts...)
}

func Stack(ctx context.Context, entity string, data map[string]any, opts ...Opt) (Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Stack(ctx, entity, data, opts...)
}

func Slice(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (map[string]bool, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Slice(ctx, entity, args, sel, opts...)
}

func Delete(ctx context.Context, entity string, data map[string]any, opts ...Opt) (bool, error) {
	c, err := dflt()
	if err != nil {
		return false, err
	}
	return c.Delete(ctx, entity, data, opts...)
}

func Purge(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (json.RawMessage, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Purge(ctx, entity, args, sel, opts...)
}

func SQLTemplate(ctx context.Context, template string, params []any, opts ...Opt) ([]Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.SQLTemplate(ctx, template, params, opts...)
}

func Query(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (json.RawMessage, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Query(ctx, xsql, params, opts...)
}

func QueryRecords(ctx context.Context, xsql string, params map[string]any, opts ...Opt) ([]Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.QueryRecords(ctx, xsql, params, opts...)
}

func QueryRecord(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.QueryRecord(ctx, xsql, params, opts...)
}

func SearchTree(ctx context.Context, entity, on string, args Args, sel Selection, opts ...Opt) ([]Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.SearchTree(ctx, entity, on, args, sel, opts...)
}

func GetTree(ctx context.Context, entity, root, on string, sel Selection, opts ...Opt) (Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.GetTree(ctx, entity, root, on, sel, opts...)
}

func GetTreeFlat(ctx context.Context, entity, root, on string, sel Selection, opts ...Opt) ([]Record, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.GetTreeFlat(ctx, entity, root, on, sel, opts...)
}

func Exec(ctx context.Context, ops []Op, opts ...Opt) ([]OpResult, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Exec(ctx, ops, opts...)
}

// ---------------------------------------------------------------------------
// Schema / introspection
// ---------------------------------------------------------------------------

func Schema(ctx context.Context, entities ...string) (map[string]any, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Schema(ctx, entities...)
}

func Lint(ctx context.Context, source string, opts ...Opt) ([]Diagnostic, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Lint(ctx, source, opts...)
}

func DeployedModel(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.DeployedModel(ctx, opts...)
}

// Deploy deploys a dataset version from a modeler export via the default
// client. Pass the export file's contents verbatim — the server decodes it.
func Deploy(ctx context.Context, exportContents string, opts ...Opt) (DeployAck, error) {
	c, err := dflt()
	if err != nil {
		return DeployAck{}, err
	}
	return c.Deploy(ctx, exportContents, opts...)
}

// Destroy destroys a dataset — every version, table and row — by xid.
func Destroy(ctx context.Context, datasetXid string, opts ...Opt) (bool, error) {
	c, err := dflt()
	if err != nil {
		return false, err
	}
	return c.Destroy(ctx, datasetXid, opts...)
}

func RuntimeModel(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.RuntimeModel(ctx, opts...)
}

// Token resolves a bearer access token via the default client's provider.
func Token(ctx context.Context, opts ...Opt) (string, error) {
	c, err := dflt()
	if err != nil {
		return "", err
	}
	return c.Token(ctx, opts...)
}

// History returns the temporal /history sub-API bound to the default client.
func History() *HistoryAPI { return mustDflt().History() }

// ---------------------------------------------------------------------------
// Streaming + live watches
// ---------------------------------------------------------------------------

// Listen streams raw /data/events. It has no error return (matching the method);
// panics with NOT_CONNECTED if called before Connect.
func Listen(ctx context.Context, opts ...Opt) *Stream[Event] {
	return mustDflt().Listen(ctx, opts...)
}

func Observe(ctx context.Context, d Descriptor, opts ...Opt) (*Stream[Event], error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Observe(ctx, d, opts...)
}

func Watch(ctx context.Context, interest WatchInterest, opts ...Opt) (*WatchHandle, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Watch(ctx, interest, opts...)
}

// WatchSchema opens a model-deploy notice stream. No error return (matching the
// method); panics with NOT_CONNECTED if called before Connect.
func WatchSchema(ctx context.Context) *SchemaWatch { return mustDflt().WatchSchema(ctx) }

func WatchQuery(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (*QueryWatch, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.WatchQuery(ctx, entity, args, sel, opts...)
}

func WatchQueryXSQL(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (*QueryWatch, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.WatchQueryXSQL(ctx, xsql, params, opts...)
}

func WatchSqlTemplate(ctx context.Context, template string, params []any, opts ...Opt) (*SqlTemplateWatch, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.WatchSqlTemplate(ctx, template, params, opts...)
}

// ---------------------------------------------------------------------------
// Subscriptions (advanced — prefer the Watch family)
// ---------------------------------------------------------------------------

func Subscribe(ctx context.Context, d Descriptor, opts ...Opt) (string, error) {
	c, err := dflt()
	if err != nil {
		return "", err
	}
	return c.Subscribe(ctx, d, opts...)
}

func Unsubscribe(ctx context.Context, handle string) error {
	c, err := dflt()
	if err != nil {
		return err
	}
	return c.Unsubscribe(ctx, handle)
}

func SubscribeModel(ctx context.Context, raw bool) error {
	c, err := dflt()
	if err != nil {
		return err
	}
	return c.SubscribeModel(ctx, raw)
}

func UnsubscribeModel(ctx context.Context, raw bool) error {
	c, err := dflt()
	if err != nil {
		return err
	}
	return c.UnsubscribeModel(ctx, raw)
}

func SetSubscriptions(ctx context.Context, items []Descriptor, opts ...Opt) error {
	c, err := dflt()
	if err != nil {
		return err
	}
	return c.SetSubscriptions(ctx, items, opts...)
}

func ClearSubscriptions(ctx context.Context) error {
	c, err := dflt()
	if err != nil {
		return err
	}
	return c.ClearSubscriptions(ctx)
}

func Subscriptions(ctx context.Context) (map[string]any, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	return c.Subscriptions(ctx)
}
