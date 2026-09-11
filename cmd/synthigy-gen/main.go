// Command synthigy-gen generates a typed Go client layer from a Synthigy
// data model. It PULLS the contract (schema + op IR) straight from the server
// — no Node, no separate tool — then emits typed structs + methods.
//
// The typical Go workflow: put your .xsql op files in ./synthigy, add one
// directive, and `go generate ./...` does the rest:
//
//	//go:generate go run github.com/synthigy/go/cmd/synthigy-gen -pull
//
//	# or, with the binary installed (go install .../cmd/synthigy-gen@latest):
//	//go:generate synthigy-gen -pull
//
// Flags:
//
//	synthigy-gen [-endpoint URL] [-xsql DIR] [-out DIR] [-package NAME] [-pull] [-check]
//	  -endpoint  server URL      (default $SYNTHIGY_ENDPOINT or http://localhost:7887)
//	  -xsql      .xsql + snapshot dir   (default ./synthigy)
//	  -out       generated-code dir     (default ./gen)
//	  -package   package name           (default: base name of -out)
//	  -pull      re-pull schema + IR from the server (else generate from the
//	             committed ./synthigy/{schema.json,ops.ir.json} snapshots)
//	  -check     CI drift gate: exit 1 when the .xsql sources no longer hash to
//	             the IR's sourceHash. Offline — no server, no credentials.
//
// Auth for -pull comes from the environment: SYNTHIGY_CLIENT_ID +
// SYNTHIGY_CLIENT_SECRET (client-credentials), or SYNTHIGY_TOKEN (static
// bearer), or neither for an authless dev server.
//
// Commit the inputs (.xsql + schema.json + ops.ir.json); gitignore the
// generated output and regenerate on demand. Output is gofmt'd; stdlib only.
//
// Legacy positional form (still supported): synthigy-gen <ir.json> <schema.json> <out.go>
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	synthigy "github.com/synthigy/go"
)

// ── IR / schema shapes (subset we consume) ──────────────────────────────────

type ir struct {
	Operations []op `json:"operations"`
}

type op struct {
	Name      string          `json:"name"`
	Op        string          `json:"op"`
	Entity    string          `json:"entity"`
	Namespace string          `json:"namespace"`
	Params    []param         `json:"params"`
	Source    string          `json:"source"`
	Watch     json.RawMessage `json:"watch"`
	Result    result          `json:"result"`
	Batch     bool            `json:"batch"`
}

type param struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Array    bool   `json:"array"`
	Optional bool   `json:"optional"`
}

type result struct {
	Kind   string  `json:"kind"`
	Fields []field `json:"fields"`
}

type field struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	Kind        string  `json:"kind"` // "", "relation", "map"
	Nullable    *bool   `json:"nullable"`
	Optional    bool    `json:"optional"`
	Cardinality string  `json:"cardinality"`
	Value       string  `json:"value"` // map value type
	Fields      []field `json:"fields"`
}

type schema struct {
	Entities map[string]entity `json:"entities"`
}

type entity struct {
	Name       string              `json:"name"`
	Skins      map[string]string   `json:"skins"`
	Attributes map[string]attr     `json:"attributes"`
	Relations  map[string]relation `json:"relations"`
}

type attr struct {
	Type  string            `json:"type"`
	Skins map[string]string `json:"skins"`
}

type relation struct {
	To          string            `json:"to"`
	Cardinality string            `json:"cardinality"`
	Skins       map[string]string `json:"skins"`
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	endpoint := flag.String("endpoint", envOr("SYNTHIGY_ENDPOINT", "http://localhost:7887"), "Synthigy server URL")
	xsqlDir := flag.String("xsql", "./synthigy", "directory of .xsql op files + committed snapshots")
	outDir := flag.String("out", "./gen", "output directory for generated Go")
	pkg := flag.String("package", "", "package name (default: base name of -out)")
	pull := flag.Bool("pull", false, "re-pull schema + IR from the server before generating")
	check := flag.Bool("check", false, "offline drift gate: fail when .xsql sources no longer match the IR's sourceHash")
	flag.Parse()
	args := flag.Args()

	// Legacy positional form: synthigy-gen <ir.json> <schema.json> <out.go>
	if len(args) == 3 {
		name := *pkg
		if name == "" {
			name = "generated"
		}
		emit(args[0], args[1], args[2], name)
		return
	}
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: synthigy-gen [-endpoint URL] [-xsql DIR] [-out DIR] [-package NAME] [-pull] [-check]")
		fmt.Fprintln(os.Stderr, "   or: synthigy-gen <ir.json> <schema.json> <out.go>   (legacy)")
		os.Exit(2)
	}

	schemaPath := filepath.Join(*xsqlDir, "schema.json")
	irPath := filepath.Join(*xsqlDir, "ops.ir.json")
	name := *pkg
	if name == "" {
		name = filepath.Base(*outDir)
	}

	// Contract between the .xsql sources on disk and the committed IR: the IR
	// carries the hash of the sources it was described from. Mismatch = stale.
	// -check gates on it offline; a plain run only warns, so `go generate` keeps
	// working without a server.
	if drifted, savedHash := sourcesDrifted(*xsqlDir, irPath); drifted {
		msg := "sources drifted from " + irPath
		if savedHash == "" {
			msg = irPath + " predates source hashing"
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s — run: synthigy-gen -pull -xsql %s\n", msg, *xsqlDir)
			os.Exit(1)
		}
		if !*pull {
			fmt.Fprintf(os.Stderr, "warning: %s — generating from the stale IR; re-pull to refresh\n", msg)
		}
	} else if *check {
		if !exists(irPath) {
			fmt.Fprintf(os.Stderr, "no %s — run: synthigy-gen -pull -xsql %s\n", irPath, *xsqlDir)
			os.Exit(1)
		}
		fmt.Printf("%s in sync with the .xsql sources\n", irPath)
		return
	}

	// Pull when asked, or when a snapshot is missing (first run).
	if *pull || !exists(schemaPath) || !exists(irPath) {
		if err := doPull(*endpoint, *xsqlDir, schemaPath, irPath); err != nil {
			fmt.Fprintf(os.Stderr, "pull failed: %v\n", err)
			os.Exit(1)
		}
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	emit(irPath, schemaPath, filepath.Join(*outDir, "generated.go"), name)
}

// doPull fetches the IAM-filtered schema (GET /schema) and describes every
// .xsql in xsqlDir into the op IR (POST /data describe), writing both snapshots
// next to the .xsql source. All native — the Go SDK owns auth + HTTP.
func doPull(endpoint, xsqlDir, schemaPath, irPath string) error {
	cfg := synthigy.Config{Endpoint: endpoint}
	if id := os.Getenv("SYNTHIGY_CLIENT_ID"); id != "" {
		cfg.ClientID = id
		cfg.ClientSecret = os.Getenv("SYNTHIGY_CLIENT_SECRET")
	} else if tok, ok := os.LookupEnv("SYNTHIGY_TOKEN"); ok {
		cfg.Token = tok
		cfg.StaticToken = true
	}
	c, err := synthigy.New(cfg)
	if err != nil {
		return err
	}
	ctx := context.Background()

	sch, err := c.Schema(ctx)
	if err != nil {
		return fmt.Errorf("GET /schema: %w", err)
	}
	if err := writeJSON(schemaPath, sch); err != nil {
		return err
	}

	src, err := readXSQL(xsqlDir)
	if err != nil {
		return err
	}
	res, err := c.Exec(ctx, []synthigy.Op{synthigy.OpDescribe(src)})
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if len(res) == 0 || !res[0].OK {
		return fmt.Errorf("describe returned no IR")
	}
	// res[0].Data is `{operations:[...]}` — re-indent for a clean committed diff.
	var irAny map[string]any
	if err := json.Unmarshal(res[0].Data, &irAny); err != nil {
		return fmt.Errorf("decode IR: %w", err)
	}
	irAny["sourceHash"] = sourceHash(src)
	if err := writeJSON(irPath, irAny); err != nil {
		return err
	}

	ver := ""
	if v, ok := sch["version"].(string); ok && v != "" {
		ver = " @" + v
	}
	ents := 0
	if e, ok := sch["entities"].(map[string]any); ok {
		ents = len(e)
	}
	var probe ir
	_ = json.Unmarshal(res[0].Data, &probe)
	fmt.Printf("pulled %d entities%s, %d ops → %s\n", ents, ver, countOps(&probe), xsqlDir)
	return nil
}

func sourceHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])
}

// sourcesDrifted reports whether the .xsql files in xsqlDir still hash to the
// sourceHash recorded in the IR, plus the saved hash ("" when the IR predates
// hashing). Unreadable sources or a missing IR are not drift — the caller
// pulls in those cases.
func sourcesDrifted(xsqlDir, irPath string) (bool, string) {
	src, err := readXSQL(xsqlDir)
	if err != nil {
		return false, ""
	}
	b, err := os.ReadFile(irPath)
	if err != nil {
		return false, ""
	}
	var saved struct {
		SourceHash string `json:"sourceHash"`
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return false, ""
	}
	return saved.SourceHash != sourceHash(src), saved.SourceHash
}

// readXSQL concatenates every *.xsql file in dir (sorted) into one source, the
// same way the server's describe expects independent, uniquely-named ops.
func readXSQL(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".xsql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no .xsql files in %s", dir)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "\n\n"), nil
}

// emit loads the IR + schema snapshots and writes gofmt'd typed Go.
func emit(irPath, schemaPath, outPath, pkg string) {
	var I ir
	mustJSON(irPath, &I)
	var S schema
	mustJSON(schemaPath, &S)

	src := generate(pkg, &I, &S)
	formatted, err := format.Source([]byte(src))
	if err != nil {
		// Emit the unformatted source alongside the error so the failure is debuggable.
		_ = os.WriteFile(outPath+".broken", []byte(src), 0o644)
		fmt.Fprintf(os.Stderr, "generated code does not compile as Go (wrote %s.broken): %v\n", outPath, err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, formatted, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d entities, %d ops)\n", outPath, len(S.Entities), countOps(&I))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func countOps(I *ir) int {
	n := 0
	for _, o := range I.Operations {
		if !o.Batch {
			n++
		}
	}
	return n
}

func mustJSON(path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.Unmarshal(b, v); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		os.Exit(1)
	}
}

// ── codegen ──────────────────────────────────────────────────────────────────

// nsData accumulates everything emitted under one namespace.
type nsData struct {
	name        string // Pascal
	writeEntity string // snake entity name, or "" for a facade (sql-template only)
	methods     []string
}

type generator struct {
	pkg     string
	schema  *schema
	structs []string // top-level struct definitions (result types + nested)
	seen    map[string]bool
}

func generate(pkg string, I *ir, S *schema) string {
	g := &generator{pkg: pkg, schema: S, seen: map[string]bool{}}

	// ── Layer 1: read + Write struct for every entity ──
	entNames := make([]string, 0, len(S.Entities))
	for n := range S.Entities {
		entNames = append(entNames, n)
	}
	sort.Strings(entNames)

	var layer1 []string
	entityByPascal := map[string]string{}
	for _, n := range entNames {
		entityByPascal[entityPascal(S, n)] = n
		layer1 = append(layer1, g.entityStructs(n, S.Entities[n]))
	}

	// ── Layer 2: overlay declared ops onto their namespace ──
	nsMap := map[string]*nsData{}
	order := []string{}
	nsOf := func(name string) *nsData {
		if nsMap[name] == nil {
			nsMap[name] = &nsData{name: name, writeEntity: entityByPascal[name]}
			order = append(order, name)
		}
		return nsMap[name]
	}
	// Seed a namespace for every entity so writes are emitted even without an op.
	for _, n := range entNames {
		nsOf(entityPascal(S, n))
	}

	for _, o := range I.Operations {
		if o.Batch {
			continue
		}
		nsName := o.Namespace
		if nsName == "" {
			nsName = o.Entity
		}
		if nsName == "" {
			fmt.Fprintf(os.Stderr, "op %q: sql-template with no root entity needs @namespace\n", o.Name)
			os.Exit(1)
		}
		g.emitOp(nsOf(entityPascal(S, nsName)), o)
	}

	// ── assemble ──
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by synthigy-gen. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"context\"\n\t\"encoding/json\"\n\n\tsynthigy \"github.com/synthigy/go\"\n)\n\n")
	b.WriteString("// Link is a relation reference by identity, used in write payloads.\ntype Link struct {\n\tXid string `json:\"xid\"`\n}\n\n")
	b.WriteString("// toMap converts a typed struct to the wire map via its json tags.\nfunc toMap(v any) map[string]any {\n\tvar m map[string]any\n\tb, _ := json.Marshal(v)\n\t_ = json.Unmarshal(b, &m)\n\treturn m\n}\n\n")

	b.WriteString("// ── Layer 1: entity read + write structs ──\n\n")
	b.WriteString(strings.Join(layer1, "\n\n"))
	b.WriteString("\n\n")

	if len(g.structs) > 0 {
		b.WriteString("// ── Layer 2: operation result + param types ──\n\n")
		b.WriteString(strings.Join(g.structs, "\n\n"))
		b.WriteString("\n\n")
	}

	b.WriteString("// ── Namespaces ──\n\n")
	sort.Strings(order)
	var nsFields []string
	for _, name := range order {
		ns := nsMap[name]
		b.WriteString(g.namespaceType(ns))
		b.WriteString("\n\n")
		nsFields = append(nsFields, fmt.Sprintf("\t%s %sNS", name, name))
	}

	// Single-client surface: an aggregate of empty namespace structs, exposed as
	// a ready-to-use package var. No client is threaded — every op runs on the
	// process-wide default installed by synthigy.Connect. A struct (not loose
	// package vars) keeps namespace names off the package scope, where they would
	// collide with the entity read types.
	fmt.Fprintf(&b, "// APISurface is the typed single-client surface: one field per entity/@namespace.\ntype APISurface struct {\n%s\n}\n\n", strings.Join(nsFields, "\n"))
	fmt.Fprintf(&b, "// API is the ready-to-use typed surface. Every op runs on the process-wide\n// default client installed by synthigy.Connect:\n//\n//\tgen.API.Project.List(ctx, gen.ProjectListParams{})\nvar API APISurface\n")

	return b.String()
}

// entityStructs emits the read struct and the Write struct for one entity.
func (g *generator) entityStructs(name string, e entity) string {
	P := entityPascal(g.schema, name)
	attrPascal := func(a string) string { return skinOr(e.Attributes[a].Skins, "pascal", pascal(a)) }
	relPascalOf := func(r string) string { return skinOr(e.Relations[r].Skins, "pascal", pascal(r)) }
	// deterministic attribute order
	attrNames := make([]string, 0, len(e.Attributes))
	for a := range e.Attributes {
		attrNames = append(attrNames, a)
	}
	sort.Strings(attrNames)
	relNames := make([]string, 0, len(e.Relations))
	for r := range e.Relations {
		relNames = append(relNames, r)
	}
	sort.Strings(relNames)

	// A foreign-key attribute and its relation often share a name (e.g.
	// `created_by`). On the write side the relation Link form wins, so skip a
	// scalar attribute that collides with a relation's Go field name.
	relPascal := map[string]bool{}
	for _, r := range relNames {
		relPascal[relPascalOf(r)] = true
	}

	var read, write []string
	readSeen := map[string]bool{"Xid": true}
	writeSeen := map[string]bool{"Xid": true}
	read = append(read, "\tXid string `json:\"xid\"`")
	write = append(write, "\tXid *string `json:\"xid,omitempty\"`")
	for _, a := range attrNames {
		if a == "xid" {
			continue
		}
		F := attrPascal(a)
		gt := goType(e.Attributes[a].Type)
		if !readSeen[F] {
			readSeen[F] = true
			read = append(read, fmt.Sprintf("\t%s %s `json:%q`", F, ptr(gt), a))
		}
		if !writeSeen[F] && !relPascal[F] {
			writeSeen[F] = true
			write = append(write, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", F, ptr(gt), a))
		}
	}
	// Write-side relations are link lists ({xid}); read-side full relations are
	// left to projection (op result) types, not the flat entity struct.
	for _, r := range relNames {
		F := relPascalOf(r)
		if writeSeen[F] {
			continue
		}
		writeSeen[F] = true
		write = append(write, fmt.Sprintf("\t%s []Link `json:\"%s,omitempty\"`", F, r))
	}

	return fmt.Sprintf("type %s struct {\n%s\n}\n\ntype %sWrite struct {\n%s\n}",
		P, strings.Join(read, "\n"), P, strings.Join(write, "\n"))
}

// emitOp adds an op's types + method to its namespace.
func (g *generator) emitOp(ns *nsData, o op) {
	// Fail loud on a missing/unknown op kind rather than defaulting to search:
	// a contract without "op" (e.g. a stale IR generated before the field
	// existed) would otherwise misroute sql-template ops to the search path and
	// fail at runtime with MISSING_ENTITY.
	switch o.Op {
	case "search", "get", "sql-template":
	case "":
		fmt.Fprintf(os.Stderr, "op %q: IR is missing the \"op\" kind (stale contract?) — re-pull with `synthigy-gen -pull`\n", o.Name)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "op %q: unknown op kind %q (expected search|get|sql-template)\n", o.Name, o.Op)
		os.Exit(1)
	}

	P := ns.name
	typeName := P + pascal(o.Name) // MovieList, DashboardStats, MovieCount
	method := pascal(o.Name)
	isSQL := o.Op == "sql-template"

	// result type
	if isSQL {
		g.addStruct(g.plainResultStruct(typeName, o.Result))
	} else {
		g.projectionStruct(typeName, o.Entity, o.Result.Fields)
	}

	// params type
	hasParams := len(o.Params) > 0
	paramsType := ""
	if hasParams {
		paramsType = typeName + "Params"
		g.addStruct(g.paramsStruct(paramsType, o.Params))
	}

	// method
	var sig, body string
	// STRICT wire: XSQL travels as a DOCUMENT — embed the @verb header so
	// the server derives verb/entity/selections/args from the source.
	src := backquote(o.Source)
	if !isSQL {
		src = backquote("@" + o.Op + " " + o.Name + "\n" + o.Source)
	}
	optsPass := "opts..."
	switch {
	case isSQL:
		paramsExpr := "nil"
		if hasParams {
			paramsExpr = "params.toParams()"
		}
		sig = g.methodSig(method, paramsType, hasParams, fmt.Sprintf("([]%s, error)", typeName))
		body = fmt.Sprintf("return synthigy.SQLTemplateAs[%s](ctx,%s, %s, %s)", typeName, src, paramsExpr, optsPass)
	case o.Op == "get":
		paramsExpr := "nil"
		if hasParams {
			paramsExpr = "params.toParams()"
		}
		wire := "append(opts, synthigy.WireOp(\"get\"))..."
		sig = g.methodSig(method, paramsType, hasParams, fmt.Sprintf("(*%s, error)", typeName))
		body = fmt.Sprintf("return synthigy.QueryOneAs[%s](ctx,%s, %s, %s)", typeName, src, paramsExpr, wire)
	default: // search
		paramsExpr := "nil"
		if hasParams {
			paramsExpr = "params.toParams()"
		}
		wire := "append(opts, synthigy.WireOp(\"search\"))..."
		sig = g.methodSig(method, paramsType, hasParams, fmt.Sprintf("([]%s, error)", typeName))
		body = fmt.Sprintf("return synthigy.QueryAs[%s](ctx,%s, %s, %s)", typeName, src, paramsExpr, wire)
	}
	ns.methods = append(ns.methods, fmt.Sprintf("func (%sNS) %s {\n\t%s\n}", P, sig, body))

	// typed watch — op marked @watch. XSQL reads → QueryWatchOf[T] (interest
	// auto-derived from rows); sql-templates → SqlTemplateWatchOf[T] (needs the
	// declared entities). Snapshot accessors are typed; events stay untyped.
	if on, wents := watchInfo(o.Watch); on {
		wmethod := "Watch" + method
		paramsExpr := "nil"
		if hasParams {
			paramsExpr = "params.toParams()"
		}
		switch {
		case isSQL:
			switch {
			case hasParams:
				ns.methods = append(ns.methods, fmt.Sprintf("// %s skipped: WatchSqlTemplate takes positional params; %q has named params", wmethod, o.Name))
			case len(wents) == 0:
				ns.methods = append(ns.methods, fmt.Sprintf("// %s skipped: sql-template @watch declares no entities", wmethod))
			default:
				quoted := make([]string, len(wents))
				for i, e := range wents {
					quoted[i] = fmt.Sprintf("%q", e)
				}
				entsOpt := "synthigy.Entities(" + strings.Join(quoted, ", ") + ")"
				wsig := g.methodSig(wmethod, paramsType, hasParams, fmt.Sprintf("(*synthigy.SqlTemplateWatchOf[%s], error)", typeName))
				wbody := fmt.Sprintf("return synthigy.WatchSQLTemplateAs[%s](ctx,%s, nil, append(opts, %s)...)", typeName, src, entsOpt)
				ns.methods = append(ns.methods, fmt.Sprintf("func (%sNS) %s {\n\t%s\n}", P, wsig, wbody))
			}
		case o.Op == "get":
			wire := fmt.Sprintf("append(opts, synthigy.WireOp(\"get\"), synthigy.Entity(%q))...", o.Entity)
			wsig := g.methodSig(wmethod, paramsType, hasParams, fmt.Sprintf("(*synthigy.QueryWatchOf[%s], error)", typeName))
			wbody := fmt.Sprintf("return synthigy.WatchQueryAs[%s](ctx,%s, %s, %s)", typeName, src, paramsExpr, wire)
			ns.methods = append(ns.methods, fmt.Sprintf("func (%sNS) %s {\n\t%s\n}", P, wsig, wbody))
		default: // search
			wire := fmt.Sprintf("append(opts, synthigy.WireOp(\"search\"), synthigy.Entity(%q))...", o.Entity)
			wsig := g.methodSig(wmethod, paramsType, hasParams, fmt.Sprintf("(*synthigy.QueryWatchOf[%s], error)", typeName))
			wbody := fmt.Sprintf("return synthigy.WatchQueryAs[%s](ctx,%s, %s, %s)", typeName, src, paramsExpr, wire)
			ns.methods = append(ns.methods, fmt.Sprintf("func (%sNS) %s {\n\t%s\n}", P, wsig, wbody))
		}
	}
}

// watchInfo reads the IR op's `watch` field: `true` → (true, nil); an array of
// entity names → (true, names); anything else → (false, nil).
func watchInfo(raw json.RawMessage) (bool, []string) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "false" {
		return false, nil
	}
	if s == "true" {
		return true, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return len(arr) > 0, arr
	}
	return false, nil
}

func (g *generator) methodSig(method, paramsType string, hasParams bool, ret string) string {
	if hasParams {
		return fmt.Sprintf("%s(ctx context.Context, params %s, opts ...synthigy.Opt) %s", method, paramsType, ret)
	}
	return fmt.Sprintf("%s(ctx context.Context, opts ...synthigy.Opt) %s", method, ret)
}

// namespaceType emits the NS struct, its write methods, and its read methods.
func (g *generator) namespaceType(ns *nsData) string {
	var out []string
	out = append(out, fmt.Sprintf("// %sNS groups the typed operations for %q. Its methods run on the\n// process-wide default client installed by synthigy.Connect.\ntype %sNS struct{}", ns.name, ns.name, ns.name))
	out = append(out, ns.methods...)
	if ns.writeEntity != "" {
		P := ns.name
		e := ns.writeEntity
		out = append(out,
			fmt.Sprintf("// Sync upserts %s, REPLACING relation link-sets. Writes are silent by\n// default — the Record is {\"count\": n}. Pass synthigy.Returning() for the\n// written record, or mint the id up front with synthigy.NewXID().\nfunc (%sNS) Sync(ctx context.Context, data %sWrite, opts ...synthigy.Opt) (synthigy.Record, error) {\n\treturn synthigy.Sync(ctx, %q, toMap(data), opts...)\n}", e, P, P, e),
			fmt.Sprintf("// Stack writes %s additively — relation links are ADDED, never removed.\n// Same returning contract as Sync.\nfunc (%sNS) Stack(ctx context.Context, data %sWrite, opts ...synthigy.Opt) (synthigy.Record, error) {\n\treturn synthigy.Stack(ctx, %q, toMap(data), opts...)\n}", e, P, P, e),
			fmt.Sprintf("func (%sNS) Delete(ctx context.Context, xid string, opts ...synthigy.Opt) (bool, error) {\n\treturn synthigy.Delete(ctx, %q, map[string]any{\"xid\": xid}, opts...)\n}", P, e),
		)
	}
	return strings.Join(out, "\n\n")
}

// projectionStruct emits a struct for a read op's result, recursing into
// relations as nested named structs.
func (g *generator) projectionStruct(typeName, entityName string, fields []field) {
	var lines []string
	seen := map[string]bool{}
	for _, f := range fields {
		fn := pascal(f.Key)
		if f.Kind == "map" {
			fn = mapFieldName(f.Key)
		}
		if seen[fn] {
			continue
		}
		seen[fn] = true
		switch f.Kind {
		case "relation":
			nested := typeName + pascal(f.Key)
			g.projectionStruct(nested, g.relTarget(entityName, f.Key), f.Fields)
			t := "*" + nested
			if f.Cardinality == "many" {
				t = "[]" + nested
			}
			lines = append(lines, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", pascal(f.Key), t, f.Key))
		case "map":
			t := "map[string]any"
			if f.Value == "int" {
				t = "map[string]int64"
			}
			lines = append(lines, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", mapFieldName(f.Key), t, f.Key))
		default:
			gt := goType(f.Type)
			if f.Nullable != nil && !*f.Nullable {
				lines = append(lines, fmt.Sprintf("\t%s %s `json:%q`", pascal(f.Key), gt, f.Key))
			} else {
				lines = append(lines, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", pascal(f.Key), ptr(gt), f.Key))
			}
		}
	}
	g.addStruct(fmt.Sprintf("type %s struct {\n%s\n}", typeName, strings.Join(lines, "\n")))
}

func (g *generator) plainResultStruct(typeName string, r result) string {
	if len(r.Fields) == 0 {
		return fmt.Sprintf("type %s = map[string]any", typeName)
	}
	var lines []string
	for _, f := range r.Fields {
		gt := goType(f.Type)
		if f.Nullable != nil && !*f.Nullable {
			lines = append(lines, fmt.Sprintf("\t%s %s `json:%q`", pascal(f.Key), gt, f.Key))
		} else {
			lines = append(lines, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", pascal(f.Key), ptr(gt), f.Key))
		}
	}
	return fmt.Sprintf("type %s struct {\n%s\n}", typeName, strings.Join(lines, "\n"))
}

func (g *generator) paramsStruct(typeName string, params []param) string {
	var lines []string
	for _, p := range params {
		gt := goType(p.Type)
		if p.Array {
			gt = "[]" + gt
		}
		// Optional params are pointers so an unset value is omitted from the map.
		if p.Optional && !p.Array {
			gt = "*" + gt
		}
		lines = append(lines, fmt.Sprintf("\t%s %s `json:\"%s,omitempty\"`", pascal(p.Name), gt, p.Name))
	}
	return fmt.Sprintf("type %s struct {\n%s\n}\n\nfunc (p %s) toParams() map[string]any { return toMap(p) }",
		typeName, strings.Join(lines, "\n"), typeName)
}

func (g *generator) addStruct(def string) {
	// Guard against duplicate type names from repeated nesting paths.
	name := structName(def)
	if name != "" {
		if g.seen[name] {
			return
		}
		g.seen[name] = true
	}
	g.structs = append(g.structs, def)
}

func (g *generator) relTarget(entityName, relKey string) string {
	if e, ok := g.schema.Entities[entityName]; ok {
		if r, ok := e.Relations[relKey]; ok && r.To != "" {
			return r.To
		}
	}
	return relKey
}

// ── helpers ──────────────────────────────────────────────────────────────────

func goType(t string) string {
	switch t {
	case "int":
		return "int64"
	case "float", "decimal", "currency":
		return "float64"
	case "bool", "boolean":
		return "bool"
	case "string", "uuid", "timestamp", "date", "time", "enum", "encrypted", "hashed", "phone", "email", "avatar":
		return "string"
	case "json":
		return "any"
	default:
		return "any"
	}
}

func ptr(t string) string {
	if t == "any" {
		return t // *any is pointless; leave as any
	}
	return "*" + t
}

func mapFieldName(key string) string {
	// "_count" → "Count", "_agg" → "Agg"
	return pascal(strings.TrimPrefix(key, "_"))
}

// skinOr prefers the server-rendered skin (docs/plans/PLAN-SCHEMA-SKINS-PROJECTION.md)
// over the local splitter — falls back only when the schema carries no entry.
func skinOr(skins map[string]string, format, fallback string) string {
	if v, ok := skins[format]; ok && v != "" {
		return v
	}
	return fallback
}

func entityPascal(S *schema, n string) string {
	return skinOr(S.Entities[n].Skins, "pascal", pascal(n))
}

func pascal(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		// Preserve an already-capitalized acronym-ish token, else Title-case.
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	// Go convention: initialisms — light touch, just ensure exported.
	return out
}

func ptrOf(b bool) *bool { return &b }

func backquote(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	// Fall back to a normal quoted string with escapes if the source has backticks.
	return fmt.Sprintf("%q", s)
}

// structName extracts the type name from a "type X struct {" or "type X = ..." def.
func structName(def string) string {
	def = strings.TrimSpace(def)
	if !strings.HasPrefix(def, "type ") {
		return ""
	}
	rest := strings.TrimPrefix(def, "type ")
	for i, r := range rest {
		if r == ' ' || r == '\t' {
			return rest[:i]
		}
	}
	return ""
}

var _ = ptrOf // reserved for future nullable-literal needs
