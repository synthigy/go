package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPascal(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user", "User"},
		{"music_album", "MusicAlbum"},
		{"music-album", "MusicAlbum"},
		{"music album", "MusicAlbum"},
		{"MusicAlbum", "MusicAlbum"},
		{"_count", "Count"}, // via leading-underscore split
		{"", "X"},           // empty → placeholder
	}
	for _, tc := range cases {
		if got := pascal(tc.in); got != tc.want {
			t.Errorf("pascal(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExported(t *testing.T) {
	// A pascal skin is authored data: a lowercase first rune emits an
	// unexported field, which encoding/json drops and `go vet` rejects.
	cases := []struct{ in, want string }{
		{"rucOTF", "RucOTF"},
		{"RUC", "RUC"},
		{"title", "Title"},
		{"", "X"},
	}
	for _, tc := range cases {
		if got := exported(tc.in); got != tc.want {
			t.Errorf("exported(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGoType(t *testing.T) {
	cases := map[string]string{
		"int":       "int64",
		"float":     "float64",
		"currency":  "float64",
		"bool":      "bool",
		"boolean":   "bool",
		"string":    "string",
		"timestamp": "string",
		"email":     "string",
		"json":      "any",
		"weird":     "any", // unknown → any
	}
	for in, want := range cases {
		if got := goType(in); got != want {
			t.Errorf("goType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapFieldName(t *testing.T) {
	if got := mapFieldName("_count"); got != "Count" {
		t.Errorf("mapFieldName(_count) = %q", got)
	}
	if got := mapFieldName("_agg"); got != "Agg" {
		t.Errorf("mapFieldName(_agg) = %q", got)
	}
}

// TestGenerateProducesValidGo is the golden guard: a minimal schema + IR must
// emit Go that (a) parses, (b) declares the entity read/write structs, and
// (c) declares the namespace type with its typed read method + write verbs.
func TestGenerateProducesValidGo(t *testing.T) {
	S := &schema{Entities: map[string]entity{
		"music_album": {
			Name:       "Music Album",
			Attributes: map[string]attr{"title": {Type: "string"}, "plays": {Type: "int"}},
			Relations:  map[string]relation{"tracks": {To: "music_track", Cardinality: "many"}},
		},
	}}
	I := &ir{Operations: []op{
		{
			Name:   "list",
			Op:     "search",
			Entity: "music_album",
			Source: "music_album\n  title\n  plays",
			Params: []param{{Name: "limit", Type: "int", Optional: true}},
			Result: result{Kind: "list", Fields: []field{
				{Key: "title", Type: "string"},
				{Key: "plays", Type: "int"},
			}},
		},
	}}

	src := generate("gen", I, S)

	// (a) must be syntactically valid Go
	if _, err := parser.ParseFile(token.NewFileSet(), "generated.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated code does not parse: %v\n---\n%s", err, src)
	}

	// (b) + (c) key declarations present
	wants := []string{
		"package gen",
		"type MusicAlbum ",          // read struct (entity-backed namespace alias/type)
		"type MusicAlbumWrite ",     // write input struct
		"type MusicAlbumNS ",        // namespace type
		"func (MusicAlbumNS) List(", // typed read method for the list op
		"func (MusicAlbumNS) Sync(", // schema-derived write verb
		"func (MusicAlbumNS) Delete(",
		"func (MusicAlbumNS) SyncMany(ctx context.Context, data []MusicAlbumWrite",
		"func (MusicAlbumNS) StackMany(",
		"func toMaps[T any]",
		"var API APISurface", // single-client surface
	}
	for _, w := range wants {
		if !strings.Contains(src, w) {
			t.Errorf("generated code missing %q\n---\n%s", w, src)
		}
	}
}

func TestWatchInfo(t *testing.T) {
	cases := []struct {
		in       string
		wantOn   bool
		wantEnts []string
	}{
		{`true`, true, nil},
		{`false`, false, nil},
		{`null`, false, nil},
		{``, false, nil},
		{`["movie","user_rating"]`, true, []string{"movie", "user_rating"}},
		{`[]`, false, []string{}}, // empty array → not a watch
	}
	for _, tc := range cases {
		on, ents := watchInfo([]byte(tc.in))
		if on != tc.wantOn {
			t.Errorf("watchInfo(%q) on = %v, want %v", tc.in, on, tc.wantOn)
		}
		if len(ents) != len(tc.wantEnts) {
			t.Errorf("watchInfo(%q) ents = %v, want %v", tc.in, ents, tc.wantEnts)
		}
	}
}

// TestGenerateWatchAndXSQLAndSQLTemplate exercises the op-emission branches:
// a @watch search op (→ QueryWatchOf), an xsql op with nested relation
// projection, and a @watch sql-template op (→ SqlTemplateWatchOf, plain result
// struct). All must parse and produce the expected typed watch signatures.
func TestGenerateWatchAndXSQLAndSQLTemplate(t *testing.T) {
	S := &schema{Entities: map[string]entity{
		"movie": {
			Name:       "Movie",
			Attributes: map[string]attr{"title": {Type: "string"}},
			Relations:  map[string]relation{"actors": {To: "movie_actor", Cardinality: "many"}},
		},
		"movie_actor": {Name: "Movie Actor", Attributes: map[string]attr{"name": {Type: "string"}}},
	}}
	nn := false
	I := &ir{Operations: []op{
		{
			Name:   "list",
			Op:     "search",
			Entity: "movie",
			Watch:  []byte(`true`), // @watch → QueryWatchOf
			Result: result{Kind: "list", Fields: []field{
				{Key: "title", Type: "string"},
				// nested relation projection → recursive projectionStruct + relTarget
				{Key: "actors", Kind: "relation", Cardinality: "many", Fields: []field{
					{Key: "name", Type: "string", Nullable: &nn},
				}},
			}},
		},
		{
			Name:      "stats",
			Op:        "sql-template",
			Namespace: "dashboard",
			Source:    "SELECT count(*) AS n FROM {movie}",
			Watch:     []byte(`["movie"]`), // @watch with entities → SqlTemplateWatchOf
			Result: result{Kind: "list", Fields: []field{
				{Key: "n", Type: "int", Nullable: &nn},
			}},
		},
	}}

	src := generate("gen", I, S)
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated code does not parse: %v\n---\n%s", err, src)
	}

	wants := []string{
		"QueryWatchOf[",        // typed watch for the search op
		"SqlTemplateWatchOf[",  // typed watch for the sql-template op
		"MovieListActors",      // nested relation projection struct name
		"type DashboardStats ", // sql-template plain result struct
		"func (DashboardNS) ",  // @namespace routed the sql-template op
	}
	for _, w := range wants {
		if !strings.Contains(src, w) {
			t.Errorf("generated code missing %q\n---\n%s", w, src)
		}
	}
}

func TestGenerateBatchOp(t *testing.T) {
	S := &schema{Entities: map[string]entity{
		"movie": {Name: "Movie", Attributes: map[string]attr{"title": {Type: "string"}}},
	}}
	nn := false
	I := &ir{Operations: []op{
		{Name: "list", Op: "search", Entity: "movie", Source: "movie\n  title",
			Params: []param{{Name: "limit", Type: "int", Optional: true}},
			Result: result{Kind: "list", Fields: []field{{Key: "title", Type: "string"}}}},
		{Name: "detail", Op: "get", Entity: "movie", Source: "movie (xid = ?xid:string)\n  title",
			Params: []param{{Name: "xid", Type: "string"}},
			Result: result{Kind: "one", Fields: []field{{Key: "title", Type: "string"}}}},
		{Name: "stats", Op: "sql-template", Namespace: "dashboard", Source: "SELECT count(*) AS n FROM {movie}",
			Result: result{Kind: "list", Fields: []field{{Key: "n", Type: "int", Nullable: &nn}}}},
		{Name: "overview", Batch: true, Members: []string{"movie/list", "detail", "dashboard/stats"}},
	}}
	src := generate("gen", I, S)
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated batch does not parse: %v\n---\n%s", err, src)
	}
	flat := strings.Join(strings.Fields(src), " ")
	for _, w := range []string{
		"func (APISurface) Overview(ctx context.Context, params OverviewBatchParams, opts ...synthigy.Opt) (*OverviewBatch, error)",
		"List []MovieList ListErr error",
		"Detail *MovieDetail DetailErr error",
		"synthigy.ResultOneAs[MovieDetail](rs[1])",
		"synthigy.OpSQLTemplate(`SELECT count(*) AS n FROM {movie}`, p)",
		"type OverviewBatchParams struct { Limit *int64 `json:\"limit,omitempty\"` Xid string", // xid required in detail → not a pointer
	} {
		if !strings.Contains(flat, w) {
			t.Errorf("generated batch missing %q\n---\n%s", w, src)
		}
	}
}

func TestGenerateNoWritesSkipsInputTier(t *testing.T) {
	S := &schema{Entities: map[string]entity{
		"movie": {Name: "Movie", Attributes: map[string]attr{"title": {Type: "string"}}},
	}}
	I := &ir{Operations: []op{}}
	src := generate("gen", I, S)
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", src, parser.AllErrors); err != nil {
		t.Fatalf("empty-op generate must still parse: %v", err)
	}
	// A model with entities but no ops still emits the entity structs + surface.
	if !strings.Contains(src, "type Movie ") {
		t.Errorf("entity struct should be emitted even with no ops")
	}
}

func TestSourcesDrifted(t *testing.T) {
	dir := t.TempDir()
	xsql := filepath.Join(dir, "a.xsql")
	ir := filepath.Join(dir, "ops.ir.json")
	if err := os.WriteFile(xsql, []byte("@search list\nmovie\n  xid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(ir, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src, err := readXSQL(dir)
	if err != nil {
		t.Fatal(err)
	}
	write(`{"sourceHash":"` + sourceHash(src) + `","operations":[]}`)
	if drifted, _ := sourcesDrifted(dir, ir); drifted {
		t.Error("matching hash should not report drift")
	}

	if err := os.WriteFile(xsql, []byte("@search list\nmovie\n  xid\n  title\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if drifted, saved := sourcesDrifted(dir, ir); !drifted || saved == "" {
		t.Errorf("edited source should drift with the saved hash reported, got %v %q", drifted, saved)
	}

	// An IR from before hashing existed drifts with an empty saved hash, so the
	// caller can say "predates hashing" instead of "you edited the sources".
	write(`{"operations":[]}`)
	if drifted, saved := sourcesDrifted(dir, ir); !drifted || saved != "" {
		t.Errorf("unhashed IR should drift with an empty saved hash, got %v %q", drifted, saved)
	}

	// No IR yet (first run) is not drift — the caller pulls.
	if err := os.Remove(ir); err != nil {
		t.Fatal(err)
	}
	if drifted, _ := sourcesDrifted(dir, ir); drifted {
		t.Error("missing IR should not report drift")
	}
}

func TestReadXSQLRecursesSortedAndMerges(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"b/z.xsql": "@workspace w2\nZ", "a.xsql": "@workspace w1\nA", "b.xsql": "B"}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, err := readXSQL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := "@workspace w1\nA\n\nB\n\nZ"; src != want {
		t.Errorf("got %q, want %q", src, want)
	}
}

// A @sync/@stack/@delete in the .xsql would exit "unknown op kind"; writes come
// from the schema, so codegen skips them like the other four SDKs do.
func TestGenerateSkipsMutationOps(t *testing.T) {
	S := &schema{Entities: map[string]entity{
		"movie": {Name: "Movie", Attributes: map[string]attr{"title": {Type: "string"}}},
	}}
	I := &ir{Operations: []op{
		{Name: "list", Op: "search", Entity: "movie", Source: "movie\n  title",
			Result: result{Kind: "list", Fields: []field{{Key: "title", Type: "string"}}}},
		{Name: "save", Op: "sync", Entity: "movie", Source: "@sync save\nmovie"},
		{Name: "add", Op: "stack", Entity: "movie", Source: "@stack add\nmovie"},
		{Name: "drop", Op: "delete", Entity: "movie", Source: "@delete drop\nmovie"},
	}}
	src := generate("gen", I, S)
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", src, parser.AllErrors); err != nil {
		t.Fatalf("does not parse: %v\n---\n%s", err, src)
	}
	for _, w := range []string{"func (MovieNS) List(", "func (MovieNS) Sync(", "func (MovieNS) Stack("} {
		if !strings.Contains(src, w) {
			t.Errorf("missing %q", w)
		}
	}
	for _, bad := range []string{"MovieSave", "MovieAdd", "MovieDrop"} {
		if strings.Contains(src, bad) {
			t.Errorf("mutation op emitted: %q", bad)
		}
	}
}
