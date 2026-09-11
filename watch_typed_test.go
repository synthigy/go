package synthigy

import "testing"

type movieRow struct {
	Title string `json:"title"`
	Plays int    `json:"plays"`
}

func TestRecordsAsDecodesTyped(t *testing.T) {
	rs := []Record{
		{"title": "Dune", "plays": 42},
		{"title": "Arrival", "plays": 7},
	}
	out := recordsAs[movieRow](rs)
	if len(out) != 2 {
		t.Fatalf("expected 2 typed rows, got %d", len(out))
	}
	if out[0].Title != "Dune" || out[0].Plays != 42 {
		t.Errorf("row 0 wrong: %+v", out[0])
	}
	if out[1].Title != "Arrival" || out[1].Plays != 7 {
		t.Errorf("row 1 wrong: %+v", out[1])
	}
}

func TestRecordsAsEmptyIsNonNil(t *testing.T) {
	out := recordsAs[movieRow](nil)
	if out == nil || len(out) != 0 {
		t.Fatalf("empty input → empty non-nil slice, got %v", out)
	}
	out = recordsAs[movieRow]([]Record{})
	if out == nil || len(out) != 0 {
		t.Fatalf("empty slice → empty non-nil slice, got %v", out)
	}
}

func TestQueryWatchOfListInitial(t *testing.T) {
	// A typed view wrapping a QueryWatch with a preset snapshot decodes the
	// records through the json round-trip on List()/Initial().
	qw := &QueryWatch{
		records: map[string]Record{"m-1": {"title": "Dune", "plays": 1}},
		initial: []Record{{"title": "Dune", "plays": 1}},
	}
	view := &QueryWatchOf[movieRow]{qw}
	list := view.List()
	if len(list) != 1 || list[0].Title != "Dune" {
		t.Fatalf("typed List() wrong: %+v", list)
	}
	init := view.Initial()
	if len(init) != 1 || init[0].Plays != 1 {
		t.Fatalf("typed Initial() wrong: %+v", init)
	}
}

func TestSqlTemplateWatchOfValueFirst(t *testing.T) {
	sw := &SqlTemplateWatch{value: []Record{{"title": "A", "plays": 5}}}
	view := &SqlTemplateWatchOf[movieRow]{sw}
	if v := view.Value(); len(v) != 1 || v[0].Title != "A" {
		t.Fatalf("typed Value() wrong: %+v", v)
	}
	if f := view.First(); f == nil || f.Plays != 5 {
		t.Fatalf("typed First() wrong: %+v", f)
	}

	empty := &SqlTemplateWatchOf[movieRow]{&SqlTemplateWatch{value: []Record{}}}
	if empty.First() != nil {
		t.Fatalf("First() on empty should be nil")
	}
}
