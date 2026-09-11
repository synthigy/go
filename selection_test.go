package synthigy

import (
	"encoding/json"
	"testing"
)

// normJSON normalizes a value through the wire to compare structure.
func normJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestNormalizeSelectionScalars(t *testing.T) {
	got := normalizeSelection(Selection{"name": nil, "email": true})
	want := map[string]any{"name": nil, "email": nil}
	if normJSON(t, got) != normJSON(t, want) {
		t.Errorf("got %s want %s", normJSON(t, got), normJSON(t, want))
	}
}

func TestNormalizeSelectionFields(t *testing.T) {
	got := normalizeSelection(Fields("name", "email"))
	want := map[string]any{"name": nil, "email": nil}
	if normJSON(t, got) != normJSON(t, want) {
		t.Errorf("got %s want %s", normJSON(t, got), normJSON(t, want))
	}
}

func TestNormalizeSelectionNestedRelationShorthand(t *testing.T) {
	got := normalizeSelection(Selection{
		"name":  nil,
		"roles": Selection{"name": nil},
	})
	want := map[string]any{
		"name": nil,
		// A bare relation travels as written — the server defaults it LEFT.
		"roles": []any{map[string]any{
			"selections": map[string]any{"name": nil},
		}},
	}
	if normJSON(t, got) != normJSON(t, want) {
		t.Errorf("got %s want %s", normJSON(t, got), normJSON(t, want))
	}
}

func TestNormalizeSelectionRelWithArgsAlias(t *testing.T) {
	got := normalizeSelection(Selection{
		"roles": Rel(Selection{"name": nil},
			WithArgs(Args{"_limit": 5}), WithAlias("activeRoles")),
	})
	want := map[string]any{
		"roles": []any{map[string]any{
			"selections": map[string]any{"name": nil},
			// caller args pass through unchanged — nothing is injected
			"args":  map[string]any{"_limit": 5},
			"alias": "activeRoles",
		}},
	}
	if normJSON(t, got) != normJSON(t, want) {
		t.Errorf("got %s want %s", normJSON(t, got), normJSON(t, want))
	}
}

// asArgsMap coerces an "args" value (typed Args or plain map) into a
// map[string]any — test-local helper (prod code no longer needs it).
func asArgsMap(v any) map[string]any {
	switch a := v.(type) {
	case map[string]any:
		return a
	case Args:
		return map[string]any(a)
	default:
		return nil
	}
}

// relArgs pulls the args map of the first config under key in a normalized
// selection, for asserting _join behavior.
func relArgs(t *testing.T, got any, key string) map[string]any {
	t.Helper()
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("not a map: %s", normJSON(t, got))
	}
	arr, ok := m[key].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("no rel configs under %q: %s", key, normJSON(t, got))
	}
	cfg := arr[0].(map[string]any)
	return asArgsMap(cfg["args"]) // nil when the config carries no args
}

func TestNoJoinInjectedForAnyShorthand(t *testing.T) {
	// FLAT-LEFT DECREE: join semantics are the server's. No relation
	// shorthand form gains a _join — selections travel as written.
	for _, sel := range []Selection{
		{"roles": Selection{"name": nil}},      // shorthand object
		{"roles": Fields("name")},              // Fields
		{"roles": []string{"name"}},            // []string
		{"roles": Rel(Selection{"name": nil})}, // Rel, no args
		{"roles": map[string]any{"name": nil}}, // raw map
	} {
		if args := relArgs(t, normalizeSelection(sel), "roles"); args["_join"] != nil {
			t.Errorf("unexpected injected _join %s for %v", normJSON(t, args), sel)
		}
	}
}

func TestExplicitJoinPreserved(t *testing.T) {
	got := normalizeSelection(Selection{
		"roles": Rel(Selection{"name": nil}, WithArgs(Args{"_join": "inner"})),
	})
	if j := relArgs(t, got, "roles")["_join"]; j != "inner" {
		t.Errorf("explicit _join:inner not preserved, got %v", j)
	}
}

func TestMaybePassesThroughWithoutJoin(t *testing.T) {
	got := normalizeSelection(Selection{
		"roles": Rel(Selection{"name": nil}, WithArgs(Args{"_maybe": map[string]any{}})),
	})
	if _, has := relArgs(t, got, "roles")["_join"]; has {
		t.Errorf("_join must never be injected: %s", normJSON(t, got))
	}
}

func TestNestedRelationsStayBare(t *testing.T) {
	// No join is injected at any nesting depth.
	got := normalizeSelection(Selection{
		"roles": Selection{
			"name":   nil,
			"scopes": Selection{"name": nil},
		},
	})
	if j := relArgs(t, got, "roles")["_join"]; j != nil {
		t.Errorf("outer relation gained _join: %s", normJSON(t, got))
	}
	// The inner relation lives under roles[0].selections.scopes.
	rolesSel := got.(map[string]any)["roles"].([]any)[0].(map[string]any)["selections"]
	if j := relArgs(t, rolesSel, "scopes")["_join"]; j != nil {
		t.Errorf("nested relation gained _join: %s", normJSON(t, got))
	}
}

func TestAggregateKeysSkipLeftJoin(t *testing.T) {
	// _count / _agg subtrees must stay untouched like everything else.
	got := normalizeSelection(Selection{
		"title":  nil,
		"_count": Selection{"actors": nil},
		"_agg":   Selection{"ratings": Selection{"rating": Selection{"_avg": nil}}},
	}).(map[string]any)
	for _, key := range []string{"_count", "_agg"} {
		arr := got[key].([]any)
		cfg := arr[0].(map[string]any)
		if _, has := asArgsMap(cfg["args"])["_join"]; has {
			t.Errorf("%s should not carry _join: %s", key, normJSON(t, cfg))
		}
	}
	// And the relation inside _agg's subtree must also be untouched.
	aggSel := got["_agg"].([]any)[0].(map[string]any)["selections"].(map[string]any)
	ratings := aggSel["ratings"].([]any)[0].(map[string]any)
	if _, has := asArgsMap(ratings["args"])["_join"]; has {
		t.Errorf("relation inside _agg should not carry _join: %s", normJSON(t, ratings))
	}
}

func TestNormalizeSelectionMultipleRels(t *testing.T) {
	got := normalizeSelection(Selection{
		"roles": []RelationConfig{
			Rel(Selection{"name": nil}, WithAlias("a")),
			Rel(Selection{"name": nil}, WithAlias("b")),
		},
	})
	arr, ok := got.(map[string]any)["roles"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2 rel configs, got %s", normJSON(t, got))
	}
}

func TestFilterHelpers(t *testing.T) {
	if normJSON(t, Eq(true)) != `{"_eq":true}` {
		t.Errorf("Eq: %s", normJSON(t, Eq(true)))
	}
	if normJSON(t, Ge(5)) != `{"_ge":5}` {
		t.Errorf("Ge: %s", normJSON(t, Ge(5)))
	}
	if normJSON(t, In("a", "b")) != `{"_in":["a","b"]}` {
		t.Errorf("In: %s", normJSON(t, In("a", "b")))
	}
	// In flattens a slice argument.
	if normJSON(t, In([]string{"a", "b"})) != `{"_in":["a","b"]}` {
		t.Errorf("In(slice): %s", normJSON(t, In([]string{"a", "b"})))
	}
	if normJSON(t, IsNull()) != `{"_is_null":true}` {
		t.Errorf("IsNull: %s", normJSON(t, IsNull()))
	}
}

// No op ever injects _join; slice pins it because the server once treated
// an injected _join as a targeting constraint here.
func TestOpSliceSelectionHasNoJoinInjection(t *testing.T) {
	op := OpSlice("user", Args{"xid": Eq("u-1")}, Selection{"roles": Selection{"xid": nil}})
	sel := op["selections"].(map[string]any)
	rels := sel["roles"].([]any)
	cfg := rels[0].(map[string]any)
	if _, ok := cfg["args"]; ok {
		t.Fatalf("slice selection got join sugar injected: %#v", cfg)
	}
}
