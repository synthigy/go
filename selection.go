package synthigy

// Selection describes the attributes and relations to return from a /data
// operation. Scalar fields map to nil; relations map to a nested Selection
// or a Rel(...) config.
//
//	Selection{
//	    "name":  nil,
//	    "email": nil,
//	    "roles": Selection{"name": nil},                       // simple relation
//	    "teams": Rel(Selection{"name": nil}, WithArgs(Args{"_limit": 5})),
//	}
//
// Fields(...) is a shorthand for a flat scalar list.
type Selection map[string]any

// Args holds query arguments for a read op: _where, _limit, _offset,
// _order_by, _distinct, plus shorthand field conditions
// (e.g. {"active": Eq(true)}).
type Args map[string]any

// RelationConfig configures a relation selection with optional args and
// alias. Build one with Rel.
type RelationConfig struct {
	Selections any // Selection or []string
	Args       Args
	Alias      string
}

// Rel builds a relation selection config. Use the WithArgs / WithAlias mods
// for filtered or aliased relations, and pass a []RelationConfig (built from
// several Rel calls) for multiple occurrences of the same relation.
//
//	"roles": Rel(Selection{"name": nil}, WithArgs(Args{"_limit": 5})),
//	"roles": []RelationConfig{
//	    Rel(Selection{"name": nil}, WithArgs(activeArgs), WithAlias("active")),
//	    Rel(Selection{"name": nil}, WithArgs(archivedArgs), WithAlias("archived")),
//	},
func Rel(selections Selection, mods ...func(*RelationConfig)) RelationConfig {
	c := RelationConfig{Selections: selections}
	for _, m := range mods {
		m(&c)
	}
	return c
}

// WithArgs attaches filter args to a Rel.
func WithArgs(a Args) func(*RelationConfig) { return func(c *RelationConfig) { c.Args = a } }

// WithAlias attaches an output alias to a Rel.
func WithAlias(alias string) func(*RelationConfig) {
	return func(c *RelationConfig) { c.Alias = alias }
}

// Fields is a shorthand Selection for a flat list of scalar fields.
//
//	Fields("name", "email")  ==  Selection{"name": nil, "email": nil}
func Fields(names ...string) Selection {
	s := make(Selection, len(names))
	for _, n := range names {
		s[n] = nil
	}
	return s
}

// normalizeSelection converts a Selection (in any of its shorthand forms)
// into the wire shape: scalar fields map to nil; relations map to an array
// of {selections, args?, alias?} config objects. Mirrors the JS SDK's
// normalizeSelection exactly.
//
// Join semantics are the SERVER's: absent "_join" is LEFT (a selection is a
// projection and never drops parents; relation args filter the related
// rows). The client injects nothing — pass Args{"_join": "inner"} explicitly
// when the relation's existence should scope its parent.
func normalizeSelection(sel any) any {
	switch v := sel.(type) {
	case nil:
		return nil
	case bool:
		return nil // true -> include field (null on the wire)
	case []string:
		m := map[string]any{}
		for _, f := range v {
			m[f] = nil
		}
		return m
	case Selection:
		return normalizeMap(v)
	case map[string]any:
		return normalizeMap(v)
	case RelationConfig:
		return relToWire(v)
	case []RelationConfig:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = relToWire(c)
		}
		return out
	default:
		return sel
	}
}

func normalizeMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = normalizeValue(val)
	}
	return out
}

func normalizeValue(val any) any {
	switch v := val.(type) {
	case nil:
		return nil
	case bool:
		return nil
	case []string:
		return []any{map[string]any{"selections": normalizeSelection(v)}}
	case RelationConfig:
		return []any{relToWire(v)}
	case []RelationConfig:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = relToWire(c)
		}
		return out
	case Selection:
		return []any{map[string]any{"selections": normalizeMap(v)}}
	case map[string]any:
		if _, ok := v["selections"]; ok {
			return []any{relMapToWire(v)}
		}
		return []any{map[string]any{"selections": normalizeMap(v)}}
	case []any:
		if len(v) > 0 {
			if _, isStr := v[0].(string); isStr {
				return []any{map[string]any{"selections": normalizeSelection(toStringSlice(v))}}
			}
		}
		out := make([]any, len(v))
		for i, item := range v {
			switch it := item.(type) {
			case RelationConfig:
				out[i] = relToWire(it)
			case map[string]any:
				out[i] = relMapToWire(it)
			default:
				out[i] = item
			}
		}
		return out
	default:
		return val
	}
}

func relToWire(c RelationConfig) map[string]any {
	m := map[string]any{"selections": normalizeSelection(c.Selections)}
	if len(c.Args) > 0 {
		m["args"] = c.Args
	}
	if c.Alias != "" {
		m["alias"] = c.Alias
	}
	return m
}

// relMapToWire normalizes a raw rel-config map (one carrying a "selections"
// key), recursing into its nested selections.
func relMapToWire(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if s, ok := m["selections"]; ok {
		out["selections"] = normalizeSelection(s)
	}
	return out
}

func toStringSlice(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
