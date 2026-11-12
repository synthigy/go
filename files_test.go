package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func hasCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

type filesCall struct{ method, path, query, ctype, ifMatch, body string }

func filesStub(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) (*Client, *[]filesCall) {
	var calls []filesCall
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, filesCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"),
			r.Header.Get("If-Match"), string(b)})
		answer(w, r)
	}))
	return c, &calls
}

func TestFilesPut(t *testing.T) {
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/f1" {
			writeJSON(w, 412, map[string]any{"error": map[string]any{"code": "FILE_CHANGED", "message": "changed"}})
			return
		}
		writeJSON(w, 200, map[string]any{"xid": "f1", "name": "a b.txt", "size": 5, "hash": "h1",
			"status": "READY", "uploaded_by": map[string]any{"xid": "u1", "name": "jane"}, "folder": nil})
	})
	ctx := context.Background()
	fi, err := c.Files().Put(ctx, "/docs/a b.txt", strings.NewReader("hello"), ContentType("text/plain"))
	if err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[0]; got != (filesCall{"PUT", "/files", "path=%2Fdocs%2Fa+b.txt", "text/plain", "", "hello"}) {
		t.Fatalf("put = %+v", got)
	}
	if fi.XID != "f1" || fi.Size != 5 || fi.UploadedBy == nil || fi.UploadedBy.Name != "jane" || fi.Folder != nil {
		t.Fatalf("file = %+v", fi)
	}

	_, err = c.Files().Put(ctx, "f1", strings.NewReader("v2"), IfMatch("abc"))
	var e *Error
	if !errors.As(err, &e) || e.Code != "FILE_CHANGED" || e.Category != "conflict" || e.Status != 412 || (*calls)[1].ifMatch != `"abc"` {
		t.Fatalf("replace = %v, %+v", err, (*calls)[1])
	}

	if _, err := c.Files().PutNew(ctx, "loose.txt", strings.NewReader("x")); err != nil ||
		(*calls)[2].method != "POST" || (*calls)[2].query != "name=loose.txt" {
		t.Fatalf("put new = %v, %+v", err, (*calls)[2])
	}

	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	_, _ = f.Seek(4, io.SeekStart)
	if _, err := c.Files().Put(ctx, "/f.bin", f); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[3]; got.body != "456789" || got.ctype != "" {
		t.Fatalf("file put = %+v", got)
	}
	if _, err := f.Stat(); err != nil {
		t.Fatalf("put closed the caller's file: %v", err)
	}

	if _, err := c.Files().Put(ctx, "", strings.NewReader("x")); !hasCode(err, "INVALID_PATH") {
		t.Fatalf("no target: %v", err)
	}
	if _, err := c.Files().PutNew(ctx, "", strings.NewReader("x")); !hasCode(err, "NAME_REQUIRED") {
		t.Fatalf("no name: %v", err)
	}
	if _, err := c.Files().Put(ctx, "/x", strings.NewReader("a=b"), ContentType("application/x-www-form-urlencoded")); !hasCode(err, "UNSUPPORTED_MEDIA_TYPE") {
		t.Fatalf("form body: %v", err)
	}
}

func TestFilesInfoAndErrors(t *testing.T) {
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files/f2/info" || r.URL.Query().Get("path") == "/x/r.txt":
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "FILE_NOT_FOUND", "message": "No such file"}})
		case r.URL.Path == "/files/f3/info":
			writeJSON(w, 503, map[string]any{"error": map[string]any{"code": "AUDIT_UNAVAILABLE", "message": "down"}})
		case r.URL.Path == "/files/search":
			writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "INVALID_QUERY", "message": "q"}})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	if fi, err := c.Files().Info(ctx, "f2"); fi != nil || err != nil {
		t.Fatalf("hidden file = %v, %v", fi, err)
	}
	if fi, err := c.Files().Info(ctx, "/x/r.txt"); fi != nil || err != nil || (*calls)[1].path != "/files/info" {
		t.Fatalf("info by path = %v, %v, %+v", fi, err, (*calls)[1])
	}
	var e *Error
	if _, err := c.Files().Info(ctx, "f3"); !errors.As(err, &e) || e.Code != "AUDIT_UNAVAILABLE" || e.Category != "not_found" || e.Retryable {
		t.Fatalf("audit down = %v", err)
	}
	if _, err := c.Files().Search(ctx, " "); !errors.As(err, &e) || e.Code != "INVALID_QUERY" || e.Category != "validation" {
		t.Fatalf("search = %v", err)
	}
	if _, err := c.Files().List(ctx); !hasCode(err, "FILES_UNAVAILABLE") {
		t.Fatalf("no module = %v", err)
	}
	if _, err := c.Files().Versions(ctx, "/x/r.txt"); !hasCode(err, "FILE_NOT_FOUND") {
		t.Fatalf("versions of a missing path = %v", err)
	}
}

func TestFilesTimeoutSparesFileBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		time.Sleep(300 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "/info") {
			writeJSON(w, 200, map[string]any{"xid": "f1"})
			return
		}
		if r.Method == http.MethodPut {
			writeJSON(w, 200, map[string]any{"xid": "f1"})
			return
		}
		_, _ = io.WriteString(w, "late bytes")
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Token: "test-token", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := c.Files().Download(ctx, "f1")
	if err != nil {
		t.Fatalf("download under a short client timeout: %v", err)
	}
	b, _ := io.ReadAll(d.Body)
	d.Body.Close()
	if string(b) != "late bytes" {
		t.Fatalf("download = %q", b)
	}
	if _, err := c.Files().Put(ctx, "f1", strings.NewReader("v2")); err != nil {
		t.Fatalf("put under a short client timeout: %v", err)
	}
	if _, err := c.Files().Info(ctx, "f1"); err == nil {
		t.Fatal("info must still honour the client timeout")
	}
}

func TestFilesZip(t *testing.T) {
	n := 0
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		if n++; n == 2 {
			writeJSON(w, 413, map[string]any{"error": map[string]any{"code": "ZIP_TOO_LARGE", "message": "too big"}})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = io.WriteString(w, "PK")
	})
	ctx := context.Background()
	d, err := c.Files().Zip(ctx, []string{"f1", "/a/r.txt"}, []string{"/site/img/"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(d.Body)
	d.Body.Close()
	if string(b) != "PK" || d.ContentType != "application/zip" {
		t.Fatalf("zip = %q %+v", b, d)
	}
	if got := (*calls)[0]; got.method != "POST" || got.path != "/files/zip" || got.ctype != "application/json" ||
		got.body != `{"files":["f1","/a/r.txt"],"folders":["/site/img/"]}` {
		t.Fatalf("zip call = %+v", got)
	}
	if _, err := c.Files().Zip(ctx, nil, []string{"/big/"}); !hasCode(err, "ZIP_TOO_LARGE") {
		t.Fatalf("over the caps = %v", err)
	}
	if (*calls)[1].body != `{"files":[],"folders":["/big/"]}` {
		t.Fatalf("nil files must be an empty list: %s", (*calls)[1].body)
	}
}

func TestFilesDownload(t *testing.T) {
	var got http.Header
	var path, query string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path, query = r.Header.Clone(), r.URL.Path, r.URL.RawQuery
		if path == "/files/info" {
			writeJSON(w, 200, map[string]any{"xid": "f9"})
			return
		}
		if r.Header.Get("If-None-Match") == `"h1"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"h1"`)
		w.Header().Set("Content-Type", "text/plain")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 2-4/10")
			w.WriteHeader(206)
			_, _ = io.WriteString(w, "234")
			return
		}
		_, _ = io.WriteString(w, "0123456789")
	}))
	ctx := context.Background()
	d, err := c.Files().Download(ctx, "f1", ByteRange(2, 4))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(d.Body)
	d.Body.Close()
	if string(b) != "234" || !d.Partial || d.Hash != "h1" || got.Get("Range") != "bytes=2-4" {
		t.Fatalf("range = %q %+v, Range %q", b, d, got.Get("Range"))
	}
	if _, err := c.Files().Download(ctx, "f1", ByteRange(-1, 5)); err != nil || got.Get("Range") != "bytes=-5" {
		t.Fatalf("suffix range %q, %v", got.Get("Range"), err)
	}
	d, err = c.Files().Download(ctx, "f1", IfNoneMatch("h1"))
	if err != nil || !d.NotModified || d.Hash != "h1" {
		t.Fatalf("not modified = %+v, %v", d, err)
	}
	if _, err := c.Files().Download(ctx, "/a/r.txt"); err != nil || path != "/files" || query != "path=%2Fa%2Fr.txt" {
		t.Fatalf("by path %q %q, %v", path, query, err)
	}
	if _, err := c.Files().Download(ctx, "/a/r.txt", AtVersion(42)); err != nil || path != "/files/f9/versions/42" {
		t.Fatalf("version path %q, %v", path, err)
	}
}

func TestFilesListSearchMove(t *testing.T) {
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == "GET":
			writeJSON(w, 200, map[string]any{"files": []any{map[string]any{"xid": "f1", "name": "a.txt"}}})
		case r.URL.Path == "/files/folders" && r.Method == "GET":
			writeJSON(w, 200, map[string]any{"parent": "d0", "folders": []any{
				map[string]any{"xid": "d1", "name": "Sub Two", "path": "/a/Sub Two/"},
				map[string]any{"xid": "d2", "name": "other", "path": "/a/other/"}}})
		case r.URL.Path == "/files/search":
			writeJSON(w, 200, map[string]any{"truncated": true, "results": []any{
				map[string]any{"type": "folder", "xid": "d1", "name": "Sub Two", "path": "/a/Sub Two/", "public": true, "cache": 60,
					"settings": map[string]any{"public": nil, "cache": 60}},
				map[string]any{"type": "file", "xid": "f1", "name": "x.bin", "hash": "h", "path": nil}}})
		case strings.HasPrefix(r.URL.Path, "/files/folders"):
			writeJSON(w, 200, map[string]any{"xid": "d3", "name": "new", "path": "/b/new/"})
		default:
			writeJSON(w, 200, map[string]any{"xid": "f1", "name": "n.png", "folder": map[string]any{"xid": "d1", "path": "/b/"}})
		}
	})
	ctx := context.Background()
	fs := c.Files()

	all, err := fs.List(ctx, Limit(5), Offset(10))
	if err != nil || len(all.Files) != 1 || len(all.Folders) != 0 || (*calls)[0].query != "limit=5&offset=10" {
		t.Fatalf("list all = %+v, %v, %+v", all, err, (*calls)[0])
	}
	if _, err := fs.List(ctx, NoFolder(), Matching("inv")); err != nil || (*calls)[1].query != "folderless=true&q=inv" {
		t.Fatalf("no folder = %v, %+v", err, (*calls)[1])
	}
	in, err := fs.List(ctx, InFolder("/a/"), Matching("sub two"))
	if err != nil || len(in.Folders) != 1 || in.Folders[0].XID != "d1" ||
		(*calls)[2].query != "folder=%2Fa%2F&q=sub+two" || (*calls)[3].query != "parent=%2Fa%2F" {
		t.Fatalf("list folder = %+v, %v, %+v", in, err, (*calls)[2:4])
	}

	hits, err := fs.Search(ctx, "sub x", InFolder("/a/"), Limit(2))
	if err != nil || !hits.Truncated || len(hits.Results) != 2 || (*calls)[4].query != "folder=%2Fa%2F&limit=2&q=sub+x" {
		t.Fatalf("search = %+v, %v, %+v", hits, err, (*calls)[4])
	}
	if d := hits.Results[0]; d.Folder == nil || d.File != nil || d.Path != "/a/Sub Two/" || *d.Folder.Cache != 60 ||
		d.Folder.Settings.Public != nil || !d.Folder.Public {
		t.Fatalf("folder hit = %+v", d)
	}
	if h := hits.Results[1]; h.File == nil || h.File.Hash != "h" || h.Path != "" {
		t.Fatalf("file hit = %+v", h)
	}

	if fi, d, err := fs.Move(ctx, "/a/x.png", "/b/n.png"); err != nil || d != nil || fi.Folder.Path != "/b/" ||
		(*calls)[5] != (filesCall{"PATCH", "/files", "path=%2Fa%2Fx.png", "application/json", "", `{"folder":"/b/","name":"n.png"}`}) {
		t.Fatalf("move file = %+v, %v, %+v", fi, err, (*calls)[5])
	}
	if _, _, err := fs.Move(ctx, "f1", "/b/"); err != nil || (*calls)[6].path != "/files/f1" || (*calls)[6].body != `{"folder":"/b/"}` {
		t.Fatalf("move into = %v, %+v", err, (*calls)[6])
	}
	if fi, d, err := fs.Move(ctx, "/a/old/", "/b/new/"); err != nil || fi != nil || d.Path != "/b/new/" ||
		(*calls)[7] != (filesCall{"PATCH", "/files/folders", "path=%2Fa%2Fold%2F", "application/json", "", `{"name":"new","parent":"/b/"}`}) {
		t.Fatalf("move folder = %+v, %v, %+v", d, err, (*calls)[7])
	}
	if _, _, err := fs.Move(ctx, "/a/old/", "/top/"); err != nil || (*calls)[8].body != `{"name":"top","parent":"/"}` {
		t.Fatalf("folder to ROOT = %v, %+v", err, (*calls)[8])
	}
	if _, _, err := fs.Move(ctx, "f1", "n.png"); !hasCode(err, "INVALID_PATH") {
		t.Fatalf("relative target = %v", err)
	}
}

func TestFilesFoldersAndSharing(t *testing.T) {
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/versions"):
			writeJSON(w, 200, map[string]any{"xid": "f1", "versions": []any{
				map[string]any{"id": 7, "at": "2026-10-29T17:40:12.532Z", "by": "u1", "hash": "h", "size": 3, "current": true}}})
		case strings.HasSuffix(r.URL.Path, "/restore"):
			writeJSON(w, 200, map[string]any{"xid": "f1"})
		case strings.HasSuffix(r.URL.Path, "/share") || strings.HasSuffix(r.URL.Path, "/access"):
			writeJSON(w, 200, map[string]any{"read": map[string]any{"users": []any{map[string]any{"xid": "u2", "name": "bob"}}, "groups": []any{}},
				"write": map[string]any{"users": []any{}, "groups": []any{}}, "delete": map[string]any{"users": []any{}, "groups": []any{}}})
		default:
			writeJSON(w, 200, map[string]any{"xid": "d1", "path": "/a b/", "public": true, "cache": nil,
				"settings": map[string]any{"public": true, "cache": nil}})
		}
	})
	ctx := context.Background()
	fs := c.Files()
	d, err := fs.Folder(ctx, "/a b/", SetPublic(true), InheritCache())
	if err != nil || !d.Public || d.Cache != nil || d.Settings.Public == nil || !*d.Settings.Public ||
		(*calls)[0] != (filesCall{"PUT", "/files/folders", "path=%2Fa+b%2F", "application/json", "", `{"cache":null,"public":true}`}) {
		t.Fatalf("folder = %+v, %v, %+v", d, err, (*calls)[0])
	}
	if _, err := fs.Folder(ctx, "/x/"); err != nil || (*calls)[1].body != `{}` {
		t.Fatalf("plain folder = %v, %+v", err, (*calls)[1])
	}
	if err := fs.Share(ctx, "/a b/", "read", []string{"u2"}, nil); err != nil ||
		(*calls)[2] != (filesCall{"POST", "/files/folders/share", "path=%2Fa+b%2F", "application/json", "", `{"access":"read","groups":[],"users":["u2"]}`}) {
		t.Fatalf("share = %v, %+v", err, (*calls)[2])
	}
	if err := fs.Unshare(ctx, "d1", "read", nil, []string{"g1"}); err != nil || (*calls)[3].path != "/files/folders/d1/unshare" {
		t.Fatalf("unshare = %v, %+v", err, (*calls)[3])
	}
	acc, err := fs.Access(ctx, "/a b/")
	if err != nil || len(acc.Read.Users) != 1 || acc.Read.Users[0].Name != "bob" || (*calls)[4].path != "/files/folders/access" {
		t.Fatalf("access = %+v, %v", acc, err)
	}
	vs, err := fs.Versions(ctx, "f1")
	if err != nil || len(vs) != 1 || vs[0].ID != 7 || !vs[0].Current || vs[0].By != "u1" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if _, err := fs.Restore(ctx, "f1", 7); err != nil || (*calls)[6].method != "POST" || (*calls)[6].path != "/files/f1/versions/7/restore" {
		t.Fatalf("restore = %v, %+v", err, (*calls)[6])
	}
	if err := fs.Delete(ctx, "/a b/r.txt"); err != nil || (*calls)[7].path != "/files" || (*calls)[7].query != "path=%2Fa+b%2Fr.txt" {
		t.Fatalf("delete file = %v, %+v", err, (*calls)[7])
	}
	if err := fs.Delete(ctx, "/a b/"); err != nil || (*calls)[8].path != "/files/folders" || (*calls)[8].query != "path=%2Fa+b%2F" {
		t.Fatalf("delete folder = %v, %+v", err, (*calls)[8])
	}
	if err := fs.Delete(ctx, "f1"); err != nil || (*calls)[9].path != "/files/f1" {
		t.Fatalf("delete by xid = %v, %+v", err, (*calls)[9])
	}
}

func TestFilesURLAndServe(t *testing.T) {
	var gotRange, gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange, gotQuery = r.Header.Get("Range"), r.URL.RawQuery
		if r.URL.Query().Get("path") == "/gone.txt" {
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "FILE_NOT_FOUND", "message": "No such file"}})
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"h1"`)
		w.Header().Set("X-Internal", "no")
		w.Header().Set("Content-Range", "bytes 0-1/5")
		w.WriteHeader(206)
		_, _ = io.WriteString(w, "he")
	}))
	fs := c.Files()
	if u := fs.URL("/site/a b(1)*.txt"); u != c.endpoint+"/files/path/site/a%20b%281%29%2A.txt" {
		t.Fatalf("path url = %s", u)
	}
	if u := fs.URL("f1", Inline()); u != c.endpoint+"/files/f1?disposition=inline" {
		t.Fatalf("xid url = %s", u)
	}

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Range", "bytes=0-1")
	rec := httptest.NewRecorder()
	fs.Serve(rec, req, "/note.txt", Inline())
	if rec.Code != 206 || rec.Body.String() != "he" || rec.Header().Get("ETag") != `"h1"` || rec.Header().Get("X-Internal") != "" ||
		gotRange != "bytes=0-1" || gotQuery != "disposition=inline&path=%2Fnote.txt" {
		t.Fatalf("serve = %d %q %v, range %q, query %q", rec.Code, rec.Body.String(), rec.Header(), gotRange, gotQuery)
	}
	rec = httptest.NewRecorder()
	fs.Serve(rec, httptest.NewRequest("GET", "/x", nil), "/gone.txt")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "FILE_NOT_FOUND") {
		t.Fatalf("missing = %d %q", rec.Code, rec.Body.String())
	}
}

func TestFilesLinks(t *testing.T) {
	made := map[string]any{"xid": "l1", "file": "f1", "url": "http://files.test/files/link/t", "downloads": 0, "max_downloads": 2, "inline": true}
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files/info":
			writeJSON(w, 200, map[string]any{"xid": "f1"})
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case r.Method == "GET":
			writeJSON(w, 200, map[string]any{"file": "f1", "links": []any{made}})
		default:
			writeJSON(w, 201, made)
		}
	})
	ctx := context.Background()
	fs := c.Files()
	l, err := fs.Link(ctx, "/a.txt", LinkExpires(600), LinkMaxDownloads(2), Inline())
	if err != nil || l.URL != made["url"] || l.MaxDownloads == nil || *l.MaxDownloads != 2 || !l.Inline {
		t.Fatalf("link = %+v, %v", l, err)
	}
	ls, err := fs.Links(ctx, "f1")
	if err != nil || len(ls) != 1 || ls[0].XID != "l1" {
		t.Fatalf("links = %+v, %v", ls, err)
	}
	if err := fs.Unlink(ctx, l.XID); err != nil {
		t.Fatal(err)
	}
	want := []filesCall{
		{"GET", "/files/info", "path=%2Fa.txt", "", "", ""},
		{"POST", "/files/f1/links", "", "application/json", "", `{"expires":600,"inline":true,"max_downloads":2}`},
		{"GET", "/files/f1/links", "", "", "", ""},
		{"DELETE", "/files/links/l1", "", "", "", ""},
	}
	for i, w := range want {
		if (*calls)[i] != w {
			t.Fatalf("call %d = %+v, want %+v", i, (*calls)[i], w)
		}
	}
	if _, err := fs.Link(ctx, "f1"); err != nil || (*calls)[4].body != `{}` {
		t.Fatalf("plain link = %v, %+v", err, (*calls)[4])
	}
}

func TestFilesLinkRefusalsAndCanLink(t *testing.T) {
	c, calls := filesStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files/f1/links" && r.URL.RawQuery == "":
			writeJSON(w, 403, map[string]any{"error": map[string]any{"code": "LINKS_DISABLED", "message": "off"}})
		case r.URL.Path == "/files/f2/links":
			writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "INVALID_EXPIRES", "message": "bad"}})
		case r.URL.Path == "/files/links/l9":
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "LINK_NOT_FOUND", "message": "gone"}})
		default:
			writeJSON(w, 200, map[string]any{"xid": "d1", "path": "/s/", "can_link": true,
				"settings": map[string]any{"public": nil, "cache": 60, "can_link": true}})
		}
	})
	ctx := context.Background()
	fs := c.Files()
	for _, tc := range []struct {
		err       error
		code, cat string
	}{
		{func() error { _, err := fs.Link(ctx, "f1"); return err }(), "LINKS_DISABLED", "iam"},
		{func() error { _, err := fs.Link(ctx, "f2", LinkExpires(0)); return err }(), "INVALID_EXPIRES", "validation"},
		{fs.Unlink(ctx, "l9"), "LINK_NOT_FOUND", "not_found"},
	} {
		var e *Error
		if !errors.As(tc.err, &e) || e.Code != tc.code || e.Category != tc.cat {
			t.Fatalf("%s = %v", tc.code, tc.err)
		}
	}
	d, err := fs.Folder(ctx, "/s/", SetCanLink(true), SetCache(60))
	if err != nil || !d.CanLink || d.Settings.CanLink == nil || !*d.Settings.CanLink || (*calls)[3].body != `{"cache":60,"can_link":true}` {
		t.Fatalf("folder = %+v, %v, %+v", d, err, (*calls)[3])
	}
	if _, err := fs.Folder(ctx, "/s/", InheritCanLink()); err != nil || (*calls)[4].body != `{"can_link":null}` {
		t.Fatalf("inherit = %v, %+v", err, (*calls)[4])
	}
}

func TestFilesKeyFormatStaysSnake(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		writeJSON(w, 200, map[string]any{"xid": "f1", "content_type": "text/plain"})
	}))
	t.Cleanup(srv.Close)
	c, _ := New(Config{Endpoint: srv.URL, Token: "t", KeyFormat: "camel"})
	fi, err := c.Files().Info(context.Background(), "f1")
	if err != nil || fi.ContentType != "text/plain" || strings.Join(queries, "|") != "" {
		t.Fatalf("info = %+v, %v, queries %q", fi, err, queries)
	}
}

func TestFilesActingAs(t *testing.T) {
	var seen []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" as="+r.Header.Get("X-Acting-As"))
		writeJSON(w, 200, map[string]any{"xid": "XXXXXXXXXXXXXXXXXXXXXX", "name": "b.txt",
			"files": []any{}, "folders": []any{}, "versions": []any{}, "links": []any{}})
	}))
	ctx := context.Background()
	fs := c.Files()
	run := func(o ...Opt) {
		_, _ = fs.Put(ctx, "/a/b.txt", strings.NewReader("x"), o...)
		_, _ = fs.PutNew(ctx, "b.txt", strings.NewReader("x"), o...)
		if d, err := fs.Download(ctx, "/a/b.txt", append(o, AtVersion(1))...); err == nil {
			d.Body.Close()
		}
		if d, err := fs.Zip(ctx, []string{"/a/b.txt"}, nil, o...); err == nil {
			d.Body.Close()
		}
		_, _ = fs.Info(ctx, "/a/b.txt", o...)
		_, _ = fs.List(ctx, append(o, InFolder("/a/"))...)
		_, _ = fs.Search(ctx, "q", o...)
		_ = fs.Delete(ctx, "/a/b.txt", o...)
		_, _, _ = fs.Move(ctx, "/a/b.txt", "/c/", o...)
		_, _ = fs.Folder(ctx, "/a/", o...)
		_ = fs.Share(ctx, "/a/", "read", []string{"u"}, nil, o...)
		_ = fs.Unshare(ctx, "/a/", "read", []string{"u"}, nil, o...)
		_, _ = fs.Access(ctx, "/a/", o...)
		_, _ = fs.Versions(ctx, "/a/b.txt", o...)
		_, _ = fs.Restore(ctx, "/a/b.txt", 1, o...)
		_, _ = fs.Link(ctx, "/a/b.txt", o...)
		_, _ = fs.Links(ctx, "/a/b.txt", o...)
		_ = fs.Unlink(ctx, "LLLLLLLLLLLLLLLLLLLLLL", o...)
		fs.Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil), "/a/b.txt", o...)
	}

	run(ActingAs("U1"))
	if len(seen) < 19 {
		t.Fatalf("only %d requests", len(seen))
	}
	for _, s := range seen {
		if !strings.HasSuffix(s, " as=U1") {
			t.Errorf("missing X-Acting-As: %s", s)
		}
	}
	seen = nil
	run()
	for _, s := range seen {
		if !strings.HasSuffix(s, " as=") {
			t.Errorf("X-Acting-As without ActingAs: %s", s)
		}
	}
}
