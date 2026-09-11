package synthigy

// Cond is a single field condition, e.g. {"_eq": value}. Use the helpers
// (Eq, Neq, …) to build them and place them in a Where/Args map keyed by
// field name: Where{"active": Eq(true)}.
type Cond = map[string]any

// Where is a filter clause. Combine field conditions with the logical
// helpers And/Or/Not. Field keys map to a Cond; the _and/_or/_not keys take
// nested clauses.
type Where = map[string]any

// Eq matches values equal to v.
func Eq(v any) Cond { return Cond{"_eq": v} }

// Neq matches values not equal to v.
func Neq(v any) Cond { return Cond{"_neq": v} }

// Gt matches values greater than v.
func Gt(v any) Cond { return Cond{"_gt": v} }

// Ge matches values greater than or equal to v.
func Ge(v any) Cond { return Cond{"_ge": v} }

// Lt matches values less than v.
func Lt(v any) Cond { return Cond{"_lt": v} }

// Le matches values less than or equal to v.
func Le(v any) Cond { return Cond{"_le": v} }

// In matches values in the given set.
func In(vals ...any) Cond { return Cond{"_in": flatten(vals)} }

// Nin matches values not in the given set.
func Nin(vals ...any) Cond { return Cond{"_nin": flatten(vals)} }

// Like matches with a SQL LIKE pattern.
func Like(pattern string) Cond { return Cond{"_like": pattern} }

// ILike matches with a case-insensitive SQL ILIKE pattern.
func ILike(pattern string) Cond { return Cond{"_ilike": pattern} }

// IsNull matches null values.
func IsNull() Cond { return Cond{"_is_null": true} }

// IsNotNull matches non-null values.
func IsNotNull() Cond { return Cond{"_is_not_null": true} }

// And combines clauses with logical AND.
func And(clauses ...Where) Where { return Where{"_and": toAnySlice(clauses)} }

// Or combines clauses with logical OR.
func Or(clauses ...Where) Where { return Where{"_or": toAnySlice(clauses)} }

// Not negates a clause.
func Not(clause Where) Where { return Where{"_not": clause} }

// flatten splices any nested slices passed to In/Nin into a single flat
// slice — mirrors the JS helpers' `(...v) => v.flat()`.
func flatten(vals []any) []any {
	out := make([]any, 0, len(vals))
	for _, v := range vals {
		switch s := v.(type) {
		case []any:
			out = append(out, s...)
		case []string:
			for _, x := range s {
				out = append(out, x)
			}
		case []int:
			for _, x := range s {
				out = append(out, x)
			}
		default:
			out = append(out, v)
		}
	}
	return out
}

func toAnySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
