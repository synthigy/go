package synthigy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFilterBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  Cond
		want Cond
	}{
		{"Eq", Eq(1), Cond{"_eq": 1}},
		{"Neq", Neq(1), Cond{"_neq": 1}},
		{"Gt", Gt(1), Cond{"_gt": 1}},
		{"Ge", Ge(1), Cond{"_ge": 1}},
		{"Lt", Lt(1), Cond{"_lt": 1}},
		{"Le", Le(1), Cond{"_le": 1}},
		{"Like", Like("a%"), Cond{"_like": "a%"}},
		{"ILike", ILike("a%"), Cond{"_ilike": "a%"}},
		{"IsNull", IsNull(), Cond{"_is_null": true}},
		{"IsNotNull", IsNotNull(), Cond{"_is_not_null": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestInNinFlatten(t *testing.T) {
	// variadic scalars
	if got := In("a", "b"); !reflect.DeepEqual(got["_in"], []any{"a", "b"}) {
		t.Fatalf("In scalars: %v", got)
	}
	// a nested []string is spliced flat, not nested
	if got := Nin([]string{"a", "b"}); !reflect.DeepEqual(got["_nin"], []any{"a", "b"}) {
		t.Fatalf("Nin []string flatten: %v", got)
	}
	// a nested []int is spliced flat
	if got := In([]int{1, 2, 3}); !reflect.DeepEqual(got["_in"], []any{1, 2, 3}) {
		t.Fatalf("In []int flatten: %v", got)
	}
	// mixed: scalar + nested []any
	if got := In("x", []any{"y", "z"}); !reflect.DeepEqual(got["_in"], []any{"x", "y", "z"}) {
		t.Fatalf("In mixed flatten: %v", got)
	}
}

func TestLogicalCombinators(t *testing.T) {
	w := And(Where{"a": Eq(1)}, Where{"b": Eq(2)})
	clauses, _ := w["_and"].([]any)
	if len(clauses) != 2 {
		t.Fatalf("And should hold 2 clauses, got %v", w)
	}

	o := Or(Where{"a": Eq(1)})
	if _, ok := o["_or"].([]any); !ok {
		t.Fatalf("Or should key _or, got %v", o)
	}

	n := Not(Where{"a": Eq(1)})
	inner, ok := n["_not"].(Where)
	if !ok || inner["a"] == nil {
		t.Fatalf("Not should wrap the clause under _not, got %v", n)
	}
}

func TestFilterMarshalsToExpectedWire(t *testing.T) {
	// A realistic Args map must serialize to the documented predicate shape.
	args := Args{
		"active": Eq(true),
		"age":    Gt(18),
	}
	b, _ := json.Marshal(args)
	var round map[string]any
	_ = json.Unmarshal(b, &round)
	active := round["active"].(map[string]any)
	if active["_eq"] != true {
		t.Fatalf("active predicate wrong: %v", active)
	}
	age := round["age"].(map[string]any)
	if age["_gt"].(float64) != 18 {
		t.Fatalf("age predicate wrong: %v", age)
	}
}
