package synthigy

import (
	"context"
	"encoding/json"
)

// Typed watch views — thin generic wrappers over the untyped QueryWatch /
// SqlTemplateWatch. They type the snapshot accessors (List/Initial/Value/First);
// Events() stays untyped on purpose — events are deltas the consumer inspects by
// type, and a typed event channel would mean a goroutine forwarding + re-decode
// per watch. Generated code uses these; hand-written code can too.

// recordsAs re-decodes decoded records (map[string]any, wire keys) into T via a
// JSON round-trip. The generated struct's json tags match the wire keys, so this
// is lossless in practice; on a mismatch it yields the zero value rather than an
// error (the snapshot accessors have no error return, mirroring the untyped API).
func recordsAs[T any](rs []Record) []T {
	if len(rs) == 0 {
		return []T{}
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return []T{}
	}
	out, _ := decodeSlice[T](b)
	return out
}

// QueryWatchOf is a typed view over a QueryWatch. Events() and Close() are
// promoted from the embedded *QueryWatch.
type QueryWatchOf[T any] struct{ *QueryWatch }

func (q *QueryWatchOf[T]) List() []T    { return recordsAs[T](q.QueryWatch.List()) }
func (q *QueryWatchOf[T]) Initial() []T { return recordsAs[T](q.QueryWatch.Initial()) }

// WatchQueryAs opens a typed live query on the default client. Mirrors
// WatchQueryXSQL; the watch interest is auto-derived from the result rows.
func WatchQueryAs[T any](ctx context.Context, xsql string, params map[string]any, opts ...Opt) (*QueryWatchOf[T], error) {
	w, err := WatchQueryXSQL(ctx, xsql, params, opts...)
	if err != nil {
		return nil, err
	}
	return &QueryWatchOf[T]{w}, nil
}

// SqlTemplateWatchOf is a typed view over a SqlTemplateWatch.
type SqlTemplateWatchOf[T any] struct{ *SqlTemplateWatch }

func (s *SqlTemplateWatchOf[T]) Value() []T { return recordsAs[T](s.SqlTemplateWatch.Value()) }

func (s *SqlTemplateWatchOf[T]) First() *T {
	v := recordsAs[T](s.SqlTemplateWatch.Value())
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}

// WatchSQLTemplateAs opens a typed live SQL-template result on the default
// client. The watched entities must be supplied via Entities(...)
// (WatchSqlTemplate requires them).
func WatchSQLTemplateAs[T any](ctx context.Context, template string, params []any, opts ...Opt) (*SqlTemplateWatchOf[T], error) {
	w, err := WatchSqlTemplate(ctx, template, params, opts...)
	if err != nil {
		return nil, err
	}
	return &SqlTemplateWatchOf[T]{w}, nil
}
