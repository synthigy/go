package synthigy

import (
	"context"
	"encoding/json"
)

// Search returns multiple entity records matching args.
func (c *Client) Search(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) ([]Record, error) {
	r, err := c.execOne(ctx, OpSearch(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return asRecords(r.Data)
}

// Get returns a single entity record by unique-key args, or nil if none.
// Unlike Search, args are flat unique-constraint values (no implicit _eq).
func (c *Client) Get(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (Record, error) {
	r, err := c.execOne(ctx, OpGet(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return asRecord(r.Data)
}

// Aggregation note
// ----------------
// There is no standalone aggregate operation. Aggregates over a record's
// RELATIONS are operation entries on the relation key of a Search/Get
// selection; the alias is the result key:
//
//	client.Search(ctx, "movie", args, synthigy.Selection{
//	    "title": nil,
//	    "actors": []any{map[string]any{"alias": "cast", "op": "count"}},
//	    "movie-ratings": []any{
//	        map[string]any{"alias": "votes", "op": "count"},
//	        map[string]any{"alias": "rating", "op": map[string]any{"avg": "value"}},
//	    },
//	})
//	// → row["cast"] == 11, row["votes"] == 4, row["rating"] == 4.25
//
// "op" is "count" or {fn: field} with fn sum, avg, min, max or count_distinct;
// "args" ({"verified": Eq(true)}) filters the counted rows. Entries mix with
// Rel(...) configs in the same array. For aggregation that is NOT a relation
// rollup (totals across an entity, cross-entity joins, window functions,
// etc.) use SQLTemplate.

// Sync upserts an entity (replacing relations). Writes are silent by default —
// the returned Record is {"count": n}. Pass Returning() for the written
// record, or mint the id up front with NewXID.
func (c *Client) Sync(ctx context.Context, entity string, data map[string]any, opts ...Opt) (Record, error) {
	r, err := c.execOne(ctx, OpSync(entity, data, applyOpts(opts).returning), opts...)
	if err != nil {
		return nil, err
	}
	return asRecord(r.Data)
}

// Stack additively upserts an entity (adds relations without removing
// existing ones). Same returning contract as Sync.
func (c *Client) Stack(ctx context.Context, entity string, data map[string]any, opts ...Opt) (Record, error) {
	r, err := c.execOne(ctx, OpStack(entity, data, applyOpts(opts).returning), opts...)
	if err != nil {
		return nil, err
	}
	return asRecord(r.Data)
}

// WriteResult is what SyncMany/StackMany return: Count always, and the
// written records when the call passed Returning().
type WriteResult struct {
	Count   int
	Records []Record
}

// SyncMany upserts many records of one entity in ONE operation — the bulk
// form of Sync, for imports.
func (c *Client) SyncMany(ctx context.Context, entity string, records []map[string]any, opts ...Opt) (WriteResult, error) {
	return c.writeMany(ctx, OpSync(entity, records, applyOpts(opts).returning), opts)
}

// StackMany is the bulk form of Stack.
func (c *Client) StackMany(ctx context.Context, entity string, records []map[string]any, opts ...Opt) (WriteResult, error) {
	return c.writeMany(ctx, OpStack(entity, records, applyOpts(opts).returning), opts)
}

func (c *Client) writeMany(ctx context.Context, op Op, opts []Opt) (WriteResult, error) {
	r, err := c.execOne(ctx, op, opts...)
	if err != nil {
		return WriteResult{}, err
	}
	if applyOpts(opts).returning {
		recs, err := asRecords(r.Data)
		return WriteResult{Count: len(recs), Records: recs}, err
	}
	var silent struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(r.Data, &silent); err != nil {
		return WriteResult{}, newError("failed to decode write result: "+err.Error(), "INTERNAL_ERROR")
	}
	return WriteResult{Count: silent.Count}, nil
}

// Slice removes specific relations from an entity, returning a map of
// relation-name to success.
func (c *Client) Slice(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (map[string]bool, error) {
	r, err := c.execOne(ctx, OpSlice(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return decodeCuts(r.Data)
}

// Delete soft-deletes an entity identified by data, returning true on
// success.
func (c *Client) Delete(ctx context.Context, entity string, data map[string]any, opts ...Opt) (bool, error) {
	r, err := c.execOne(ctx, OpDelete(entity, data), opts...)
	if err != nil {
		return false, err
	}
	var ok bool
	if err := json.Unmarshal(r.Data, &ok); err != nil {
		return false, newError("failed to decode delete result: "+err.Error(), "INTERNAL_ERROR")
	}
	return ok, nil
}

// Purge hard-deletes entities matching args, returning the purged records.
// The server may return a single record or an array; PurgeMany returns the
// raw decoded value when you need the array form.
func (c *Client) Purge(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (json.RawMessage, error) {
	r, err := c.execOne(ctx, OpPurge(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Claim locks up to args["_limit"] free records matching args to the caller
// and returns them; records the caller already holds match too and get a new lease.
func (c *Client) Claim(ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) ([]Record, error) {
	r, err := c.execOne(ctx, OpClaim(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return asRecords(r.Data)
}

// Renew extends the lease on records the caller holds; a record missing from
// the result is no longer held. sel defaults to xid.
func (c *Client) Renew(ctx context.Context, entity string, xids []string, sel Selection, opts ...Opt) ([]Record, error) {
	if len(xids) == 0 {
		return []Record{}, nil
	}
	if sel == nil {
		sel = Selection{"xid": nil}
	}
	return c.Claim(ctx, entity, Args{"xid": In(xids), "_limit": len(xids)}, sel, opts...)
}

// Release clears the caller's locks on records named by a list of xids or a
// unique-key map, returning how many were released.
func (c *Client) Release(ctx context.Context, entity string, data any, opts ...Opt) (int, error) {
	r, err := c.execOne(ctx, OpRelease(entity, data), opts...)
	if err != nil {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(r.Data, &n); err != nil {
		return 0, newError("failed to decode release result: "+err.Error(), "INTERNAL_ERROR")
	}
	return n, nil
}

// SQLTemplate executes an ERD-aware SQL template ({entity.field} /
// {entity->rel} placeholders) and returns the result rows. params are
// positional ([]any) for ? placeholders.
func (c *Client) SQLTemplate(ctx context.Context, template string, params []any, opts ...Opt) ([]Record, error) {
	r, err := c.execOne(ctx, OpSQLTemplate(template, params), opts...)
	if err != nil {
		return nil, err
	}
	return asRecords(r.Data)
}

// Query runs an XSQL selection-DSL string with optional ?name:type[]
// placeholders. STRICT wire: XSQL travels only as the `xsql` DOCUMENT op —
// {op: "xsql", xsql: <document>, params}. A bare rooted body gets a
// synthetic `@<verb> _q` header client-side; the verb defaults to "search"
// (pass WireOp("get") for a unique-key read, which returns a single record).
// The server derives verb/entity/selections/args from the document.
//
// Query returns the raw result payload (an array for search, a single object
// for get); decode with json.Unmarshal or use QueryRecords/QueryRecord.
func (c *Client) Query(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	op := Op{"op": "xsql", "xsql": xsqlDocument(xsql, firstNonEmpty(o.op, "search"))}
	if params != nil {
		op["params"] = params
	}
	r, err := c.execOne(ctx, op, opts...)
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

// QueryRecords runs an XSQL search query and decodes the rows.
func (c *Client) QueryRecords(ctx context.Context, xsql string, params map[string]any, opts ...Opt) ([]Record, error) {
	data, err := c.Query(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return asRecords(data)
}

// QueryRecord runs an XSQL get query (pass WireOp("get")) and decodes the
// single record.
func (c *Client) QueryRecord(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (Record, error) {
	data, err := c.Query(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return asRecord(data)
}

// SearchTree searches for entities matching args, walks the `on` self-FK
// relation up to ancestors, and composes the flat result into a forest. The
// `on` relation must be present in sel. Pass Raw(true) for the flat array.
func (c *Client) SearchTree(ctx context.Context, entity, on string, args Args, sel Selection, opts ...Opt) ([]Record, error) {
	o := applyOpts(opts)
	r, err := c.execOne(ctx, OpSearchTree(entity, on, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	flat, err := asRecords(r.Data)
	if err != nil {
		return nil, err
	}
	if o.raw {
		return flat, nil
	}
	return ComposeForest(flat, on, o.childrenKey), nil
}

// GetTree returns the root plus all descendants reachable via the `on`
// self-FK relation, composed into a single nested tree. The `on` relation
// must be present in sel. Pass Raw(true) for the flat array (use GetTreeFlat).
func (c *Client) GetTree(ctx context.Context, entity, root, on string, sel Selection, opts ...Opt) (Record, error) {
	o := applyOpts(opts)
	r, err := c.execOne(ctx, OpGetTree(entity, root, on, sel), opts...)
	if err != nil {
		return nil, err
	}
	flat, err := asRecords(r.Data)
	if err != nil {
		return nil, err
	}
	return ComposeTree(flat, on, root, o.childrenKey), nil
}

// GetTreeFlat returns the raw flat descendant list from a get-tree op
// without composing it into a tree.
func (c *Client) GetTreeFlat(ctx context.Context, entity, root, on string, sel Selection, opts ...Opt) ([]Record, error) {
	r, err := c.execOne(ctx, OpGetTree(entity, root, on, sel), opts...)
	if err != nil {
		return nil, err
	}
	return asRecords(r.Data)
}

// ---- Typed (generic) read helpers ---------------------------------------
//
// Go methods can't take type parameters, so the typed variants are
// package-level functions that decode results into your structs.

// SearchAs runs Search on the default client and decodes the rows into []T.
func SearchAs[T any](ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) ([]T, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	r, err := c.execOne(ctx, OpSearch(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return decodeSlice[T](r.Data)
}

// GetAs runs Get on the default client and decodes the record into *T (nil if
// no record).
func GetAs[T any](ctx context.Context, entity string, args Args, sel Selection, opts ...Opt) (*T, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	r, err := c.execOne(ctx, OpGet(entity, args, sel), opts...)
	if err != nil {
		return nil, err
	}
	return decodePtr[T](r.Data)
}

// QueryAs runs an XSQL search query on the default client and decodes the rows
// into []T.
func QueryAs[T any](ctx context.Context, xsql string, params map[string]any, opts ...Opt) ([]T, error) {
	data, err := Query(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return decodeSlice[T](data)
}

// ResultAs decodes one Exec result into []T; a failed op is its *Error.
func ResultAs[T any](r OpResult) ([]T, error) {
	if !r.OK {
		return nil, errorFromServer(r.Error, 0, r.RequestID)
	}
	return decodeSlice[T](r.Data)
}

// ResultOneAs decodes one Exec result into *T (nil when there is no record);
// a failed op is its *Error.
func ResultOneAs[T any](r OpResult) (*T, error) {
	if !r.OK {
		return nil, errorFromServer(r.Error, 0, r.RequestID)
	}
	return decodePtr[T](r.Data)
}

// QuerySlice runs an XSQL @slice document on the default client and returns
// which relations were cut.
func QuerySlice(ctx context.Context, xsql string, params map[string]any, opts ...Opt) (map[string]bool, error) {
	data, err := Query(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return decodeCuts(data)
}

// ResultSlice decodes one Exec @slice result; a failed op is its *Error.
func ResultSlice(r OpResult) (map[string]bool, error) {
	if !r.OK {
		return nil, errorFromServer(r.Error, 0, r.RequestID)
	}
	return decodeCuts(r.Data)
}

func decodeCuts(data json.RawMessage) (map[string]bool, error) {
	out := map[string]bool{}
	if len(data) == 0 || string(data) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newError("failed to decode slice result: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

func decodeSlice[T any](data json.RawMessage) ([]T, error) {
	if len(data) == 0 || string(data) == "null" {
		return []T{}, nil
	}
	var out []T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newError("failed to decode result: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

func decodePtr[T any](data json.RawMessage) (*T, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newError("failed to decode result: "+err.Error(), "INTERNAL_ERROR")
	}
	return &out, nil
}

// QueryOneAs runs an XSQL get query (WireOp("get")) on the default client and
// decodes the single record into *T (nil if none). Companion to QueryAs for
// unique-key reads.
func QueryOneAs[T any](ctx context.Context, xsql string, params map[string]any, opts ...Opt) (*T, error) {
	data, err := Query(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return decodePtr[T](data)
}

// SQLTemplateAs runs a SQL template on the default client and decodes the rows
// into []T. Companion to SearchAs/QueryAs for typed sql-template ops.
func SQLTemplateAs[T any](ctx context.Context, template string, params any, opts ...Opt) ([]T, error) {
	c, err := dflt()
	if err != nil {
		return nil, err
	}
	r, err := c.execOne(ctx, OpSQLTemplate(template, params), opts...)
	if err != nil {
		return nil, err
	}
	return decodeSlice[T](r.Data)
}
