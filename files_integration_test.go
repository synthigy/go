//go:build integration

// Live files suite: three client-credentials clients whose service users hold
// File Admin, File User and File Reader. Skips unless
// SYNTHIGY_FILES_TEST_ENDPOINT is set.
//
//	SYNTHIGY_FILES_TEST_ENDPOINT
//	SYNTHIGY_FILES_TEST_{ADMIN,USER,READER,BFF}_{ID,SECRET} (BFF: trusted, File Admin)
//
//	go test -tags integration -run TestFilesLive -count=1 .
package synthigy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func filesLiveClient(t *testing.T, role string) *Client {
	t.Helper()
	id := os.Getenv("SYNTHIGY_FILES_TEST_" + role + "_ID")
	secret := os.Getenv("SYNTHIGY_FILES_TEST_" + role + "_SECRET")
	if id == "" || secret == "" {
		t.Fatalf("SYNTHIGY_FILES_TEST_%s_ID and _SECRET must be set", role)
	}
	c, err := New(Config{
		Endpoint:     os.Getenv("SYNTHIGY_FILES_TEST_ENDPOINT"),
		ClientID:     id,
		ClientSecret: secret,
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New %s: %v", role, err)
	}
	t.Cleanup(c.Close)
	return c
}

func expectFilesErr(t *testing.T, step string, err error, code, category string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Errorf("%s: want %s, got %v", step, code, err)
		return
	}
	if e.Code != code {
		t.Errorf("%s: code = %s (%s), want %s", step, e.Code, e.Message, code)
	}
	if category != "" && e.Category != category {
		t.Errorf("%s: category = %s, want %s", step, e.Category, category)
	}
}

func readDownload(t *testing.T, step string, f *FilesAPI, xid string, opts ...Opt) ([]byte, *FileDownload) {
	t.Helper()
	d, err := f.Download(context.Background(), xid, opts...)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	defer d.Body.Close()
	b, err := io.ReadAll(d.Body)
	if err != nil {
		t.Fatalf("%s: read: %v", step, err)
	}
	return b, d
}

func fetchLink(t *testing.T, u string) ([]byte, http.Header) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", u, resp.StatusCode, b)
	}
	return b, resp.Header
}

func hasUser(gs Grantees, xid string) bool {
	for _, u := range gs.Users {
		if u.XID == xid {
			return true
		}
	}
	return false
}

func linkStatus(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestFilesLive(t *testing.T) {
	if os.Getenv("SYNTHIGY_FILES_TEST_ENDPOINT") == "" {
		t.Skip("set SYNTHIGY_FILES_TEST_ENDPOINT to run the live files suite")
	}
	admin := filesLiveClient(t, "ADMIN")
	user := filesLiveClient(t, "USER")
	reader := filesLiveClient(t, "READER")
	bff := filesLiveClient(t, "BFF")
	af, uf, rf, bf := admin.Files(), user.Files(), reader.Files(), bff.Files()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	run := "go-" + NewXID()
	top := "/F-" + run + "/"
	bin := []byte{0x00, 0x01, 0xff, 0x7f, 0x80, 0x00, 0xfe, 0xff, 0x10, 0x20}
	next := []byte{0xff, 0x00, 0xaa, 0x55, 0x00}
	badJSON := []byte{0xff, 0xfe, 0x7b, 0x7d}

	var fileXIDs []string
	folders := []string{top}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		for _, x := range fileXIDs {
			_ = af.Delete(cctx, x)
		}
		for i := len(folders) - 1; i >= 0; i-- {
			_ = af.Delete(cctx, folders[i])
		}
	})

	// 1. admin makes the top folder and shares it.
	if F, err := af.Folder(ctx, top); err != nil || F.Path != top {
		t.Fatalf("folder: %+v, %v", F, err)
	}
	userMe, err := user.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami user: %v", err)
	}
	readerMe, err := reader.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami reader: %v", err)
	}
	for _, a := range []string{"write", "read", "delete"} {
		if err := af.Share(ctx, top, a, []string{userMe.XID}, nil); err != nil {
			t.Fatalf("share %s: %v", a, err)
		}
	}
	if err := af.Share(ctx, top, "read", []string{readerMe.XID}, nil); err != nil {
		t.Fatalf("share reader: %v", err)
	}
	acl, err := af.Access(ctx, top)
	if err != nil || !hasUser(acl.Write, userMe.XID) || !hasUser(acl.Delete, userMe.XID) ||
		!hasUser(acl.Read, readerMe.XID) || hasUser(acl.Write, readerMe.XID) {
		t.Fatalf("access: %+v, %v", acl, err)
	}

	// 2. user puts, replaces and versions.
	binPath := top + "bin-" + run + ".bin"
	first, err := uf.Put(ctx, binPath, bytes.NewReader(bin))
	if err != nil || first.Size != int64(len(bin)) {
		t.Fatalf("put: %+v, %v", first, err)
	}
	fileXIDs = append(fileXIDs, first.XID)
	if b, _ := readDownload(t, "download by path", uf, binPath); !bytes.Equal(b, bin) {
		t.Fatalf("download = %x", b)
	}
	second, err := uf.Put(ctx, binPath, bytes.NewReader(next))
	if err != nil || second.XID != first.XID || second.Hash == first.Hash {
		t.Fatalf("second put: %+v, %v", second, err)
	}
	_, err = uf.Put(ctx, first.XID, bytes.NewReader(bin), IfMatch(strings.Repeat("0", 64)))
	expectFilesErr(t, "stale if-match", err, "FILE_CHANGED", "conflict")
	_, err = uf.Put(ctx, top+"nope/x.bin", bytes.NewReader(bin))
	expectFilesErr(t, "missing folder", err, "FOLDER_NOT_FOUND", "not_found")
	_, err = uf.Put(ctx, top+`a\b`, bytes.NewReader(bin))
	expectFilesErr(t, "bad name", err, "INVALID_NAME", "validation")

	var vs []FileVersion
	for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(500 * time.Millisecond) {
		if vs, err = uf.Versions(ctx, binPath); err != nil {
			t.Fatalf("versions: %v", err)
		}
		if len(vs) >= 2 {
			break
		}
	}
	if len(vs) != 2 || !vs[0].Current || vs[1].Hash != first.Hash {
		t.Fatalf("versions = %+v", vs)
	}
	if b, _ := readDownload(t, "old version", uf, first.XID, AtVersion(vs[1].ID)); !bytes.Equal(b, bin) {
		t.Fatalf("old version = %x", b)
	}
	if r, err := uf.Restore(ctx, binPath, vs[1].ID); err != nil || r.Hash != first.Hash {
		t.Fatalf("restore: %+v, %v", r, err)
	}
	if b, _ := readDownload(t, "restored", uf, first.XID); !bytes.Equal(b, bin) {
		t.Fatalf("restored = %x", b)
	}
	if b, d := readDownload(t, "range", uf, binPath, ByteRange(2, 4)); !bytes.Equal(b, bin[2:5]) || !d.Partial {
		t.Fatalf("range = %x, %+v", b, d)
	}

	js, err := uf.Put(ctx, top+"bad-"+run+".json", bytes.NewReader(badJSON), ContentType("application/json"))
	if err != nil {
		t.Fatalf("put json: %v", err)
	}
	fileXIDs = append(fileXIDs, js.XID)
	if b, _ := readDownload(t, "json", uf, js.XID); !bytes.Equal(b, badJSON) {
		t.Fatalf("json = %x", b)
	}
	if fi, err := uf.Info(ctx, binPath); err != nil || fi.XID != first.XID || fi.Folder == nil || fi.Folder.Path != top {
		t.Fatalf("info by path: %+v, %v", fi, err)
	}
	if fi, err := uf.Info(ctx, top+"missing.bin"); err != nil || fi != nil {
		t.Fatalf("missing info: %+v, %v", fi, err)
	}
	ls, err := uf.List(ctx, InFolder(top))
	if err != nil || len(ls.Files) != 2 || len(ls.Folders) != 0 {
		t.Fatalf("list top: %+v, %v", ls, err)
	}
	if ls, err := uf.List(ctx, Matching("bin-"+run)); err != nil || len(ls.Files) != 1 || ls.Files[0].XID != first.XID {
		t.Fatalf("list q: %+v, %v", ls, err)
	}

	loose, err := uf.PutNew(ctx, "loose-"+run+".txt", strings.NewReader("loose"))
	if err != nil || loose.Folder != nil {
		t.Fatalf("put new: %+v, %v", loose, err)
	}
	fileXIDs = append(fileXIDs, loose.XID)
	twin, err := uf.PutNew(ctx, "loose-"+run+".txt", strings.NewReader("twin"))
	if err != nil || twin.XID == loose.XID {
		t.Fatalf("twin: %+v, %v", twin, err)
	}
	if err := uf.Delete(ctx, twin.XID); err != nil {
		t.Fatalf("delete twin: %v", err)
	}
	if ls, err := uf.List(ctx, NoFolder(), Matching("loose-"+run)); err != nil || len(ls.Files) != 1 || ls.Files[0].XID != loose.XID {
		t.Fatalf("no folder: %+v, %v", ls, err)
	}

	// 3. user folders and moves.
	if _, err := uf.Folder(ctx, top+"sub/"); err != nil {
		t.Fatalf("subfolder: %v", err)
	}
	folders = append(folders, top+"sub/")
	if _, d, err := uf.Move(ctx, top+"sub/", top+"sub2/"); err != nil || d.Path != top+"sub2/" {
		t.Fatalf("rename folder: %+v, %v", d, err)
	}
	folders = append(folders, top+"sub2/")
	if ls, err := uf.List(ctx, InFolder(top)); err != nil || len(ls.Folders) != 1 || ls.Folders[0].Path != top+"sub2/" {
		t.Fatalf("list folders: %+v, %v", ls, err)
	}
	if fi, _, err := uf.Move(ctx, first.XID, top+"sub2/"); err != nil || fi.Folder.Path != top+"sub2/" {
		t.Fatalf("move in: %+v, %v", fi, err)
	}
	if fi, _, err := uf.Move(ctx, top+"sub2/bin-"+run+".bin", top); err != nil || fi.Folder.Path != top {
		t.Fatalf("move out: %+v, %v", fi, err)
	}
	_, _, err = uf.Move(ctx, top+"sub2/", "/sub2-"+run+"/")
	expectFilesErr(t, "move to ROOT", err, "FORBIDDEN", "iam")
	expectFilesErr(t, "delete non-empty", uf.Delete(ctx, top), "FOLDER_NOT_EMPTY", "conflict")

	// 3b. search and list q.
	hit, err := uf.Search(ctx, "F-"+run+" sub2")
	if err != nil || len(hit.Results) != 1 || hit.Results[0].Folder == nil || hit.Results[0].Path != top+"sub2/" || hit.Truncated {
		t.Fatalf("search folder: %+v, %v", hit, err)
	}
	hit, err = uf.Search(ctx, run+" bin")
	if err != nil || len(hit.Results) != 1 || hit.Results[0].File == nil || hit.Results[0].File.XID != first.XID || hit.Results[0].Path != binPath {
		t.Fatalf("search file: %+v, %v", hit, err)
	}
	if hit, err := uf.Search(ctx, ".bin F-"+run); err != nil || len(hit.Results) != 0 {
		t.Fatalf("order matters: %+v, %v", hit, err)
	}
	all, err := uf.Search(ctx, "F-"+run)
	if err != nil || len(all.Results) < 4 || all.Results[0].Path != top {
		t.Fatalf("search all: %+v, %v", all, err)
	}
	var paged []SearchHit
	for offset, more := 0, true; more; offset += 2 {
		page, err := uf.Search(ctx, "F-"+run, Limit(2), Offset(offset))
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		paged = append(paged, page.Results...)
		more = page.Truncated
	}
	if len(paged) != len(all.Results) {
		t.Fatalf("paged %d, all %d", len(paged), len(all.Results))
	}
	if hit, err := uf.Search(ctx, "sub2", InFolder(top)); err != nil || len(hit.Results) != 1 || hit.Results[0].Path != top+"sub2/" {
		t.Fatalf("search scoped: %+v, %v", hit, err)
	}
	if hit, err := uf.Search(ctx, "loose-"+run); err != nil || len(hit.Results) != 1 || hit.Results[0].Path != "" {
		t.Fatalf("search loose: %+v, %v", hit, err)
	}
	if hit, err := rf.Search(ctx, "loose-"+run); err != nil || len(hit.Results) != 0 {
		t.Fatalf("reader search loose: %+v, %v", hit, err)
	}
	if ls, err := uf.List(ctx, InFolder(top), Matching("%")); err != nil || len(ls.Files) != 0 {
		t.Fatalf("percent plain: %+v, %v", ls, err)
	}
	if ls, err := uf.List(ctx, InFolder(top), Matching("sub")); err != nil || len(ls.Folders) != 1 {
		t.Fatalf("list q folders: %+v, %v", ls, err)
	}
	_, err = uf.Search(ctx, "  ")
	expectFilesErr(t, "blank search", err, "INVALID_QUERY", "validation")

	// 4. reader sees and downloads, may not write.
	rl, err := rf.List(ctx, InFolder("/"))
	if err != nil {
		t.Fatalf("reader list: %v", err)
	}
	seen := false
	for _, d := range rl.Folders {
		if d.Path == top {
			seen = true
			if d.Writable {
				t.Errorf("reader sees top as writable")
			}
		}
	}
	if !seen {
		t.Fatalf("reader does not list %s", top)
	}
	if b, _ := readDownload(t, "reader download", rf, first.XID); !bytes.Equal(b, bin) {
		t.Fatalf("reader download = %x", b)
	}
	if fi, err := rf.Info(ctx, loose.XID); err != nil || fi != nil {
		t.Fatalf("reader sees loose: %+v, %v", fi, err)
	}
	_, err = rf.Put(ctx, top+"r-"+run+".bin", bytes.NewReader(bin))
	expectFilesErr(t, "reader put", err, "FORBIDDEN", "iam")
	_, err = rf.Folder(ctx, top+"r-"+run+"/")
	expectFilesErr(t, "reader folder", err, "FORBIDDEN", "iam")

	// 4b. a trusted client acting as the user and the reader.
	if fi, err := bf.Info(ctx, loose.XID); err != nil || fi == nil {
		t.Fatalf("bff as itself does not see loose: %v", err)
	}
	if fi, err := bf.Info(ctx, loose.XID, ActingAs(readerMe.XID)); err != nil || fi != nil {
		t.Fatalf("bff acting as reader sees loose: %+v, %v", fi, err)
	}
	if b, _ := readDownload(t, "bff download as reader", bf, first.XID, ActingAs(readerMe.XID)); !bytes.Equal(b, bin) {
		t.Fatalf("bff download as reader = %x", b)
	}
	_, err = bf.Put(ctx, top+"bff-r-"+run+".bin", bytes.NewReader(bin), ActingAs(readerMe.XID))
	expectFilesErr(t, "bff put as reader", err, "FORBIDDEN", "iam")
	own, err := bf.Put(ctx, top+"bff-u-"+run+".bin", bytes.NewReader(bin), ActingAs(userMe.XID))
	if err != nil || own.UploadedBy == nil || own.UploadedBy.XID != userMe.XID {
		t.Fatalf("bff put as user: %+v, %v", own, err)
	}
	if err := bf.Delete(ctx, own.XID, ActingAs(userMe.XID)); err != nil {
		t.Fatalf("bff delete as user: %v", err)
	}
	_, err = af.Info(ctx, first.XID, ActingAs(readerMe.XID))
	expectFilesErr(t, "untrusted client acting", err, "NOT_TRUSTED", "")

	// 5. links for browsers.
	notePath := top + "note-" + run + ".txt"
	note, err := uf.Put(ctx, notePath, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("put note: %v", err)
	}
	fileXIDs = append(fileXIDs, note.XID)
	if s := linkStatus(t, uf.URL(note.XID)); s != 401 {
		t.Fatalf("private link = %d", s)
	}
	_, err = uf.Folder(ctx, top, SetPublic(true))
	expectFilesErr(t, "user sets public", err, "ATTRIBUTE_FORBIDDEN", "iam")
	if _, err := uf.Folder(ctx, top, SetCache(120)); err != nil {
		t.Fatalf("cache: %v", err)
	}
	shown, err := af.Folder(ctx, top, SetPublic(true))
	if err != nil || !shown.Public || shown.Cache == nil || *shown.Cache != 120 || shown.Settings.Public == nil || !*shown.Settings.Public || shown.Settings.CanLink != nil {
		t.Fatalf("public: %+v, %v", shown, err)
	}
	for _, u := range []string{uf.URL(note.XID), uf.URL(notePath)} {
		b, h := fetchLink(t, u)
		if string(b) != "hello" || h.Get("Cache-Control") != "public, max-age=120" {
			t.Fatalf("%s = %q, %v", u, b, h)
		}
	}
	if _, h := fetchLink(t, uf.URL(note.XID, Inline())); !strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
		t.Fatalf("inline = %v", h)
	}
	inner, err := af.Folder(ctx, top+"inner/")
	if err != nil || !inner.Public || inner.Settings.Public != nil {
		t.Fatalf("inner: %+v, %v", inner, err)
	}
	folders = append(folders, top+"inner/")
	secret, err := uf.Put(ctx, top+"inner/secret-"+run+".txt", strings.NewReader("secret"))
	if err != nil {
		t.Fatalf("put secret: %v", err)
	}
	fileXIDs = append(fileXIDs, secret.XID)
	if s := linkStatus(t, uf.URL(secret.XID)); s != 200 {
		t.Fatalf("inherited public = %d", s)
	}
	if _, err := af.Folder(ctx, top+"inner/", SetPublic(false)); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, uf.URL(secret.XID)); s != 401 {
		t.Fatalf("own false = %d", s)
	}
	if _, err := af.Folder(ctx, top+"inner/", InheritPublic()); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, uf.URL(secret.XID)); s != 200 {
		t.Fatalf("inherit again = %d", s)
	}
	if err := uf.Delete(ctx, secret.XID); err != nil {
		t.Fatal(err)
	}
	if err := af.Delete(ctx, top+"inner/"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Range", "bytes=0-1")
	rec := httptest.NewRecorder()
	uf.Serve(rec, req, notePath)
	if rec.Code != 206 || rec.Body.String() != "he" {
		t.Fatalf("serve = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	uf.Serve(rec, httptest.NewRequest("GET", "/x", nil), top+"gone-"+run+".txt")
	if rec.Code != 404 {
		t.Fatalf("serve missing = %d", rec.Code)
	}

	// 5b. download links.
	shut, err := uf.Put(ctx, top+"link-"+run+".txt", strings.NewReader("linked"))
	if err != nil {
		t.Fatalf("put linked: %v", err)
	}
	fileXIDs = append(fileXIDs, shut.XID)
	if _, err := af.Folder(ctx, top, SetPublic(false)); err != nil {
		t.Fatal(err)
	}
	_, err = uf.Link(ctx, shut.XID)
	expectFilesErr(t, "links off", err, "LINKS_DISABLED", "iam")
	_, err = uf.Folder(ctx, top, SetCanLink(true))
	expectFilesErr(t, "user sets can_link", err, "ATTRIBUTE_FORBIDDEN", "iam")
	if d, err := af.Folder(ctx, top, SetCanLink(true)); err != nil || !d.CanLink {
		t.Fatalf("can_link on: %+v, %v", d, err)
	}
	_, err = rf.Link(ctx, shut.XID)
	expectFilesErr(t, "reader link", err, "FORBIDDEN", "iam")
	_, err = uf.Link(ctx, shut.XID, LinkExpires(0))
	expectFilesErr(t, "expires 0", err, "INVALID_EXPIRES", "validation")

	link, err := uf.Link(ctx, shut.XID, LinkExpires(600))
	if err != nil || link.File != shut.XID || link.CreatedBy != userMe.XID || link.Downloads != 0 || link.URL == "" {
		t.Fatalf("link: %+v, %v", link, err)
	}
	if b, h := fetchLink(t, link.URL); string(b) != "linked" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("link get = %q, %v", b, h)
	}
	once, err := uf.Link(ctx, shut.XID, LinkMaxDownloads(1), Inline())
	if err != nil {
		t.Fatal(err)
	}
	rq, _ := http.NewRequest("GET", once.URL, nil)
	rq.Header.Set("Range", "bytes=1-2")
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "linked" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("limited link ignores Range: %d %q %v", resp.StatusCode, b, resp.Header)
	}
	if s := linkStatus(t, once.URL); s != 404 {
		t.Fatalf("used up = %d", s)
	}

	camel, err := New(Config{Endpoint: os.Getenv("SYNTHIGY_FILES_TEST_ENDPOINT"), ClientID: os.Getenv("SYNTHIGY_FILES_TEST_USER_ID"),
		ClientSecret: os.Getenv("SYNTHIGY_FILES_TEST_USER_SECRET"), KeyFormat: "camel", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer camel.Close()
	has := func(ls []FileLink, xid string) *FileLink {
		for i := range ls {
			if ls[i].XID == xid {
				return &ls[i]
			}
		}
		return nil
	}
	cls, err := camel.Files().Links(ctx, shut.XID)
	if err != nil {
		t.Fatal(err)
	}
	if cs := has(cls, link.XID); cs == nil || cs.CreatedBy != userMe.XID || cs.MaxDownloads != nil {
		t.Fatalf("keyFormat camel links: %+v", cls)
	}
	if ci, err := camel.Files().Info(ctx, shut.XID); err != nil || ci.ContentType == "" || ci.ContentType != shut.ContentType {
		t.Fatalf("keyFormat camel info: %+v, %v", ci, err)
	}

	mine, err := uf.Links(ctx, shut.XID)
	if err != nil {
		t.Fatal(err)
	}
	if m := has(mine, link.XID); m == nil || m.Downloads != 1 {
		t.Fatalf("mine: %+v", mine)
	}
	for _, l := range mine {
		if l.URL != "" {
			t.Fatalf("the url is shown once: %+v", l)
		}
	}
	admins, err := af.Link(ctx, shut.XID)
	if err != nil {
		t.Fatal(err)
	}
	if mine, _ := uf.Links(ctx, shut.XID); has(mine, admins.XID) != nil {
		t.Fatalf("File User sees its own only")
	}
	if all, _ := af.Links(ctx, shut.XID); has(all, link.XID) == nil {
		t.Fatalf("File Admin sees all")
	}
	expectFilesErr(t, "unlink other's", uf.Unlink(ctx, admins.XID), "LINK_NOT_FOUND", "not_found")

	if _, err := af.Folder(ctx, top, SetCanLink(false)); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, link.URL); s != 404 {
		t.Fatalf("switched off: existing links stop = %d", s)
	}
	if _, err := af.Folder(ctx, top, SetCanLink(true)); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, link.URL); s != 200 {
		t.Fatalf("switched on again = %d", s)
	}
	if err := uf.Unlink(ctx, link.XID); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, link.URL); s != 404 {
		t.Fatalf("unlinked = %d", s)
	}
	if err := uf.Delete(ctx, shut.XID); err != nil {
		t.Fatal(err)
	}
	if s := linkStatus(t, admins.URL); s != 404 {
		t.Fatalf("file deleted: its links die = %d", s)
	}
	if _, err := af.Folder(ctx, top, SetPublic(true), InheritCanLink()); err != nil {
		t.Fatal(err)
	}

	// 6. admin moves the subfolder to ROOT and deletes it.
	if _, d, err := af.Move(ctx, top+"sub2/", "/sub2-"+run+"/"); err != nil || d.Path != "/sub2-"+run+"/" {
		t.Fatalf("admin move: %+v, %v", d, err)
	}
	if err := af.Delete(ctx, "/sub2-"+run+"/"); err != nil {
		t.Fatalf("delete moved: %v", err)
	}

	// 7. cleanup.
	for _, x := range fileXIDs {
		_ = uf.Delete(ctx, x)
		if fi, err := uf.Info(ctx, x); err != nil || fi != nil {
			t.Fatalf("still there %s: %+v, %v", x, fi, err)
		}
	}
	fileXIDs = nil
	if err := af.Delete(ctx, top); err != nil {
		t.Fatalf("delete top: %v", err)
	}
	folders = nil
}
