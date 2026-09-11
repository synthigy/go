package synthigy

import (
	"reflect"
	"sort"
	"testing"
)

func TestRowKey(t *testing.T) {
	if got := rowKey(Record{"xid": "x-1"}); got != "x-1" {
		t.Errorf("xid key = %q", got)
	}
	if got := rowKey(Record{"euuid": "e-1"}); got != "e-1" {
		t.Errorf("euuid fallback = %q", got)
	}
	if got := rowKey(Record{"xid": "x-1", "euuid": "e-1"}); got != "x-1" {
		t.Errorf("xid should win over euuid, got %q", got)
	}
	if got := rowKey(nil); got != "" {
		t.Errorf("nil row → empty key, got %q", got)
	}
	if got := rowKey(Record{"name": "no-id"}); got != "" {
		t.Errorf("no id keys → empty, got %q", got)
	}
}

func TestCollectAllXidsTopLevelAndNested(t *testing.T) {
	rows := []Record{
		{"xid": "m-1", "title": "A", "actors": []any{
			map[string]any{"xid": "a-9", "name": "Z"},
		}},
		{"xid": "m-2", "genre": map[string]any{"xid": "g-3"}},
	}
	got := collectAllXids(rows)
	want := []string{"a-9", "g-3", "m-1", "m-2"} // sorted, nested included
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectAllXids = %v, want %v", got, want)
	}
}

func TestCollectAllXidsHandlesRecordSlices(t *testing.T) {
	rows := []Record{
		{"xid": "p-1", "children": []Record{{"xid": "c-1"}, {"xid": "c-2"}}},
	}
	got := collectAllXids(rows)
	sort.Strings(got)
	want := []string{"c-1", "c-2", "p-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested []Record not walked: %v, want %v", got, want)
	}
}

func TestDiffRow(t *testing.T) {
	before := Record{"title": "A", "plays": 1, "same": "x"}
	after := Record{"title": "B", "plays": 1, "same": "x", "new": "n"}
	changed, bmap, amap := diffRow(before, after)
	sort.Strings(changed)
	if !reflect.DeepEqual(changed, []string{"new", "title"}) {
		t.Fatalf("changed keys = %v, want [new title]", changed)
	}
	if bmap["title"] != "A" || amap["title"] != "B" {
		t.Errorf("before/after maps wrong: %v / %v", bmap, amap)
	}
	if _, ok := amap["new"]; !ok {
		t.Errorf("added key should appear in afterMap: %v", amap)
	}
	if _, ok := amap["plays"]; ok {
		t.Errorf("unchanged key must not appear: %v", amap)
	}
}

func TestJsonEqual(t *testing.T) {
	if !jsonEqual(map[string]any{"a": 1, "b": 2}, map[string]any{"b": 2, "a": 1}) {
		t.Error("order-independent maps should be equal")
	}
	if jsonEqual([]any{1, 2}, []any{2, 1}) {
		t.Error("differently-ordered arrays are not equal")
	}
	if !jsonEqual("x", "x") {
		t.Error("scalars equal")
	}
}

func TestHasPrefix(t *testing.T) {
	if !hasPrefix("record/update", "record/") {
		t.Error("record/ prefix")
	}
	if hasPrefix("x", "record/") {
		t.Error("short string can't have long prefix")
	}
}
