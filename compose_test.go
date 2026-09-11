package synthigy

import "testing"

func rec(xid string, parent any, extra ...any) Record {
	r := Record{"xid": xid, "father": parent}
	for i := 0; i+1 < len(extra); i += 2 {
		r[extra[i].(string)] = extra[i+1]
	}
	return r
}

func TestComposeTree(t *testing.T) {
	rows := []Record{
		rec("a", nil),
		rec("b", map[string]any{"xid": "a"}),
		rec("c", map[string]any{"xid": "a"}),
		rec("d", map[string]any{"xid": "b"}),
	}
	tree := ComposeTree(rows, "father", "a", "")
	if tree == nil {
		t.Fatal("nil tree")
	}
	children, _ := tree["_children"].([]Record)
	if len(children) != 2 {
		t.Fatalf("root should have 2 children, got %d", len(children))
	}
	// One of the children (b) should itself have one child (d).
	found := false
	for _, c := range children {
		if c["xid"] == "b" {
			gc, _ := c["_children"].([]Record)
			if len(gc) == 1 && gc[0]["xid"] == "d" {
				found = true
			}
		}
	}
	if !found {
		t.Error("nested child d not linked under b")
	}
}

func TestComposeForest(t *testing.T) {
	rows := []Record{
		rec("a", nil),
		rec("b", map[string]any{"xid": "a"}),
		rec("x", nil), // independent root
	}
	forest := ComposeForest(rows, "father", "")
	if len(forest) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(forest))
	}
}

func TestComposeTreeCycleGuard(t *testing.T) {
	rows := []Record{
		rec("a", map[string]any{"xid": "b"}),
		rec("b", map[string]any{"xid": "a"}),
	}
	// Should not infinitely recurse.
	tree := ComposeTree(rows, "father", "a", "")
	if tree == nil {
		t.Fatal("nil tree")
	}
}

func TestComposeTreeCustomChildrenKey(t *testing.T) {
	rows := []Record{rec("a", nil), rec("b", map[string]any{"xid": "a"})}
	tree := ComposeTree(rows, "father", "a", "kids")
	if _, ok := tree["kids"]; !ok {
		t.Error("custom children key not used")
	}
}

func TestParentIDCasings(t *testing.T) {
	r := Record{"xid": "c", "parent_node": map[string]any{"xid": "p"}}
	if got := parentID(r, "parent-node"); got != "p" {
		t.Errorf("parentID kebab->snake = %q, want p", got)
	}
}
