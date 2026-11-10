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

	// files
	folder         string
	contentType    string
	ifMatch        string
	ifNoneMatch    string
	byteRange      *[2]int64
	version        *int64
	fileQuery      string
	noFolder       bool
	offset         *int
	inline         bool
	folderSettings map[string]any
	linkExpires    *int
	linkMaxDl      *int
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

// Limit caps the rows of a history op, Files.List or Files.Search.
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

// InFolder narrows Files.List to one folder, or Files.Search to below it;
// pass the folder path ("/a/b/") or xid.
func InFolder(folder string) Opt { return func(o *callOptions) { o.folder = folder } }

// ContentType sets the media type Files.Put stores; omitted, the server
// guesses it from the name.
func ContentType(t string) Opt { return func(o *callOptions) { o.contentType = t } }

// IfMatch makes Files.Put refuse with FILE_CHANGED unless the stored hash is
// still this one.
func IfMatch(hash string) Opt { return func(o *callOptions) { o.ifMatch = hash } }

// IfNoneMatch makes Files.Download answer NotModified, without bytes, while
// the stored hash is still this one.
func IfNoneMatch(hash string) Opt { return func(o *callOptions) { o.ifNoneMatch = hash } }

// ByteRange downloads bytes start..end, inclusive. A negative end reads to
// the end; a negative start with end n reads the last n bytes.
func ByteRange(start, end int64) Opt {
	return func(o *callOptions) { o.byteRange = &[2]int64{start, end} }
}

// AtVersion downloads a kept version (FileVersion.ID) instead of the current
// bytes.
func AtVersion(id int64) Opt { return func(o *callOptions) { o.version = &id } }

// Matching narrows Files.List to names holding these words in order, any
// case; a space stands for anything between, % and _ are plain.
func Matching(q string) Opt { return func(o *callOptions) { o.fileQuery = q } }

// NoFolder narrows Files.List to files in no folder.
func NoFolder() Opt { return func(o *callOptions) { o.noFolder = true } }

// Offset skips the first n results of Files.List or Files.Search.
func Offset(n int) Opt { return func(o *callOptions) { o.offset = &n } }

// Inline makes a Files.URL, Files.Serve or Files.Link answer open in the
// browser instead of downloading.
func Inline() Opt { return func(o *callOptions) { o.inline = true } }

func folderSetting(key string, v any) Opt {
	return func(o *callOptions) {
		if o.folderSettings == nil {
			o.folderSettings = map[string]any{}
		}
		o.folderSettings[key] = v
	}
}

// SetPublic sets a folder's own Public in Files.Folder: files there, and in
// folders below without their own value, load without a token.
func SetPublic(v bool) Opt { return folderSetting("public", v) }

// InheritPublic clears a folder's own Public in Files.Folder.
func InheritPublic() Opt { return folderSetting("public", nil) }

// SetCache sets the seconds browsers and CDNs may keep a folder's files in
// Files.Folder.
func SetCache(seconds int) Opt { return folderSetting("cache", seconds) }

// InheritCache clears a folder's own Cache in Files.Folder.
func InheritCache() Opt { return folderSetting("cache", nil) }

// SetCanLink switches download links on or off for a folder in Files.Folder;
// File Admin only.
func SetCanLink(v bool) Opt { return folderSetting("can_link", v) }

// InheritCanLink clears a folder's own CanLink in Files.Folder.
func InheritCanLink() Opt { return folderSetting("can_link", nil) }

// LinkExpires sets the seconds a Files.Link lives: default 3600, at most 7 days.
func LinkExpires(seconds int) Opt { return func(o *callOptions) { o.linkExpires = &seconds } }

// LinkMaxDownloads caps the downloads a Files.Link allows; unset is unlimited.
func LinkMaxDownloads(n int) Opt { return func(o *callOptions) { o.linkMaxDl = &n } }
