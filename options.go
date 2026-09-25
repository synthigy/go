package synthigy

// Opt is a per-call option. Pass any number to a method's trailing
// variadic; later options win. Options that don't apply to a given method
// are ignored.
type Opt func(*callOptions)

// callOptions is the resolved set of per-call settings.
type callOptions struct {
	actingAs    string
	keyFormat   string
	audience    string
	returning   bool   // sync/stack: echo the written records
	op          string // XSQL Query wire op (default "search")
	entity      string // override root entity (Query)
	raw         bool   // tree ops: return flat array instead of composing
	childrenKey string // tree ops: nesting key (default "_children")

	// subscription / observe
	key           string
	backfill      bool
	backfillLimit int

	// watch
	entities []string // entity names a SQL template reads from
	records  []string // extra record xids to watch for updates

	// lint
	lintEntity string
	lintOp     string

	// onboard
	onboardReset      *bool
	onboardMethods    []string
	onboardTTLSeconds *int
	onboardReturnURL  *string

	// history
	tenant         any
	includeDeleted *bool
	limit          *int
	track          string
	groupBy        string
	between        []any
	cursor         any
}

func applyOpts(opts []Opt) callOptions {
	o := callOptions{childrenKey: "_children", backfillLimit: 1000}
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// ActingAs sets the impersonation target (user xid) for the call, overriding
// the client default.
func ActingAs(userXID string) Opt { return func(o *callOptions) { o.actingAs = userXID } }

// KeyFormat overrides the response key-case ("kebab", "snake", or "camel").
func KeyFormat(format string) Opt { return func(o *callOptions) { o.keyFormat = format } }

// Audience targets a token() call at a specific audience (omit for the
// default Synthigy audience).
func Audience(aud string) Opt { return func(o *callOptions) { o.audience = aud } }

// Returning makes Sync/Stack echo the written records instead of the default
// silent {"count": n}. Minting ids with NewXID is the cheaper way to know what
// you wrote.
func Returning() Opt { return func(o *callOptions) { o.returning = true } }

// WireOp overrides the XSQL Query wire op ("search" default, or "get",
// "slice", "purge").
func WireOp(op string) Opt { return func(o *callOptions) { o.op = op } }

// Entity overrides the root entity name (used by Query and Lint when the
// server can't infer it).
func Entity(name string) Opt { return func(o *callOptions) { o.entity = name } }

// Raw returns the flat result array from SearchTree/GetTree instead of
// composing it into a tree/forest.
func Raw(v bool) Opt { return func(o *callOptions) { o.raw = v } }

// ChildrenKey sets the nesting key for composed tree results (default
// "_children").
func ChildrenKey(key string) Opt { return func(o *callOptions) { o.childrenKey = key } }

// SubKey gives a subscription/observe flow a stable handle for later
// unsubscribe; defaults to a hash of the descriptor.
func SubKey(key string) Opt { return func(o *callOptions) { o.key = key } }

// Backfill replays /history events missed during an Observe disconnect.
func Backfill(v bool) Opt { return func(o *callOptions) { o.backfill = v } }

// BackfillLimit caps the number of history rows per backfill pass (default
// 1000).
func BackfillLimit(n int) Opt { return func(o *callOptions) { o.backfillLimit = n } }

// Entities lists the entity names a SQL template / query reads from, so the
// watch can subscribe to their relation xids. Required for WatchSqlTemplate.
func Entities(names ...string) Opt { return func(o *callOptions) { o.entities = names } }

// WatchRecords lists extra record xids to watch (so per-row updates on
// existing rows trigger a refresh).
func WatchRecords(xids ...string) Opt { return func(o *callOptions) { o.records = xids } }

// LintEntity sets the root entity (kebab-case) for schema-aware lint checks.
func LintEntity(name string) Opt { return func(o *callOptions) { o.lintEntity = name } }

// LintOp sets the wire op used when linting ("search" default).
func LintOp(op string) Opt { return func(o *callOptions) { o.lintOp = op } }

// OnboardReset soft-recycles the account before minting: strips its
// federated identities, nulls its password, revokes its live
// sessions/tokens. Does NOT touch `active` — that flag is the caller's data.
func OnboardReset(v bool) Opt { return func(o *callOptions) { o.onboardReset = &v } }

// OnboardMethods restricts the claim page to these methods, e.g.
// OnboardMethods("password") or OnboardMethods("google"). Omitted = every
// active federation provider plus password.
func OnboardMethods(methods ...string) Opt {
	return func(o *callOptions) { o.onboardMethods = methods }
}

// OnboardTTL sets the claim link's lifetime in seconds (server default 24h).
func OnboardTTL(seconds int) Opt {
	return func(o *callOptions) { o.onboardTTLSeconds = &seconds }
}

// OnboardReturnURL sets where a successful DIRECT claim (browser) redirects
// to, instead of Synthigy's generic status page. Must match one of the
// calling client's registered redirections (or be a loopback URI) — an
// unregistered value is rejected with RETURN_URL_NOT_REGISTERED.
func OnboardReturnURL(url string) Opt {
	return func(o *callOptions) { o.onboardReturnURL = &url }
}

// Tenant scopes a history op to a tenant.
func Tenant(t any) Opt { return func(o *callOptions) { o.tenant = t } }

// IncludeDeleted includes soft-deleted state in history.GetAt.
func IncludeDeleted(v bool) Opt { return func(o *callOptions) { o.includeDeleted = &v } }

// Limit caps the number of rows returned by a history op.
func Limit(n int) Opt { return func(o *callOptions) { o.limit = &n } }

// Track selects the audit track for history events/since ("entity", etc.).
func Track(t string) Opt { return func(o *callOptions) { o.track = t } }

// GroupBy groups history.Timeline events by ":request", ":actor", or ":scope".
func GroupBy(g string) Opt { return func(o *callOptions) { o.groupBy = g } }

// Between sets the [from, to] time bounds for history events/timeline.
// Either bound may be nil.
func Between(from, to any) Opt { return func(o *callOptions) { o.between = []any{from, to} } }

// Cursor sets the exclusive lower-bound timestamp for history.Since.
func Cursor(ts any) Opt { return func(o *callOptions) { o.cursor = ts } }
