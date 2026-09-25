package synthigy

import (
	"context"
	"encoding/json"
)

// Op is a single raw operation for Exec. Build one with the Op* helpers or
// construct the map directly.
type Op = map[string]any

// OpResult is one entry in a /data response's results array.
type OpResult struct {
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data"`
	Error     *serverError    `json:"error"`
	RequestID string          `json:"-"`
}

// dataResponse is the /data endpoint response envelope.
type dataResponse struct {
	Results []OpResult   `json:"results"`
	Error   *serverError `json:"error"`
}

// Op builders — mirror the JS SDK's `op` object. Use with Exec for batched
// or mixed operations.

func OpSearch(entity string, args Args, sel Selection) Op {
	return Op{"op": "search", "entity": entity, "args": args, "selections": normalizeSelection(sel)}
}
func OpGet(entity string, args Args, sel Selection) Op {
	return Op{"op": "get", "entity": entity, "args": args, "selections": normalizeSelection(sel)}
}

// OpSync builds a sync op for one record (a map) or many (a slice of maps).
// The server answers {"count": n}; pass returning true for the written
// records. Mint ids with NewXID when you need them up front — that is the
// cheap way to know what you wrote.
func OpSync(entity string, data any, returning ...bool) Op {
	return Op{"op": "sync", "entity": entity, "data": data,
		"returning": len(returning) > 0 && returning[0]}
}

// OpStack builds a stack op, one record or many. Same returning contract as OpSync.
func OpStack(entity string, data any, returning ...bool) Op {
	return Op{"op": "stack", "entity": entity, "data": data,
		"returning": len(returning) > 0 && returning[0]}
}
func OpSlice(entity string, args Args, sel Selection) Op {
	return Op{"op": "slice", "entity": entity, "args": args, "selections": normalizeSelection(sel)}
}
func OpDelete(entity string, data map[string]any) Op {
	return Op{"op": "delete", "entity": entity, "data": data}
}
func OpPurge(entity string, args Args, sel Selection) Op {
	return Op{"op": "purge", "entity": entity, "args": args, "selections": normalizeSelection(sel)}
}
func OpSearchTree(entity, on string, args Args, sel Selection) Op {
	return Op{"op": "search-tree", "entity": entity, "on": on, "args": args, "selections": normalizeSelection(sel)}
}
func OpGetTree(entity, root, on string, sel Selection) Op {
	return Op{"op": "get-tree", "entity": entity, "root": root, "on": on, "selections": normalizeSelection(sel)}
}
func OpSQLTemplate(template string, params any) Op {
	if params == nil {
		params = []any{}
	}
	return Op{"op": "sql-template", "template": template, "params": params}
}

// OpQuery builds an XSQL read for an Exec batch; verb names the op a bare
// body runs as (default "search").
func OpQuery(xsql string, params map[string]any, verb string) Op {
	op := Op{"op": "xsql", "xsql": xsqlDocument(xsql, firstNonEmpty(verb, "search"))}
	if params != nil {
		op["params"] = params
	}
	return op
}

func OpDeployedModel() Op { return Op{"op": "deployed-model"} }
func OpRuntimeModel() Op  { return Op{"op": "runtime-model"} }

// OpDeploy deploys a dataset version from a modeler export — pass the
// export file's contents verbatim, the server decodes it.
func OpDeploy(exportContents string) Op { return Op{"op": "deploy", "data": exportContents} }

// OpDestroy destroys a dataset (every version, table and row) by xid.
func OpDestroy(datasetXid string) Op {
	return Op{"op": "delete", "entity": "dataset", "data": map[string]any{"xid": datasetXid}}
}

// OpDescribe compiles one XSQL program into the codegen IR; the result Data is
// `{operations:[...]}`. For several .xsql files use OpDescribeFiles —
// concatenating them changes what each file's @namespace means.
func OpDescribe(source string) Op { return Op{"op": "describe", "source": source} }

// DescribeFile is one .xsql file for OpDescribeFiles.
type DescribeFile struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// OpDescribeFiles compiles several .xsql files into one IR. Each file is parsed
// on its own, so its buffer-level @namespace stays in it; a (namespace, name)
// declared twice across files fails with DUPLICATE_OPERATION.
func OpDescribeFiles(files []DescribeFile) Op { return Op{"op": "describe", "sources": files} }

// Exec runs raw operations against the /data endpoint and returns the
// per-op results. A top-level server error is returned as an *Error; per-op
// errors are surfaced on each OpResult.Error (the typed helpers in crud.go
// convert those to *Error).
func (c *Client) Exec(ctx context.Context, ops []Op, opts ...Opt) ([]OpResult, error) {
	o := applyOpts(opts)
	body := map[string]any{"operations": ops}
	if actingAs := firstNonEmpty(o.actingAs, c.defaultActingAs); actingAs != "" {
		body["acting_as"] = actingAs
	}
	if kf := firstNonEmpty(o.keyFormat, c.defaultKeyFormat); kf != "" {
		body["key_format"] = kf
	}

	raw, rid, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var dr dataResponse
	if err := json.Unmarshal(raw, &dr); err != nil {
		return nil, newError("failed to decode response: "+err.Error(), "INTERNAL_ERROR")
	}
	if dr.Error != nil {
		return nil, errorFromServer(dr.Error, 0, rid)
	}
	for i := range dr.Results {
		dr.Results[i].RequestID = rid
	}
	return dr.Results, nil
}

// execOne runs a single op and returns its result, converting a per-op error
// into an *Error (the common path for the typed CRUD helpers).
func (c *Client) execOne(ctx context.Context, op Op, opts ...Opt) (*OpResult, error) {
	results, err := c.Exec(ctx, []Op{op}, opts...)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, newError("server returned no results", "INTERNAL_ERROR")
	}
	r := &results[0]
	if !r.OK {
		return nil, errorFromServer(r.Error, 0, r.RequestID)
	}
	return r, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
