package synthigy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// FilesAPI stores, reads and shares files: the bytes behind /files, each also
// a File record on /data. Obtain it via Client.Files(). A file is named by
// its path ("/site/img/logo.png") or its xid; a folder by its path ("/site/img/").
type FilesAPI struct {
	c *Client
}

// Files returns the file accessor.
func (c *Client) Files() *FilesAPI { return &FilesAPI{c: c} }

// Named is a user, group or folder as the files endpoints name it.
type Named struct {
	XID  string `json:"xid"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// FileInfo is a file as the files endpoints answer it.
type FileInfo struct {
	XID  string `json:"xid"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Hash is the SHA-256 of the bytes, hex; also the download ETag.
	Hash        string `json:"hash"`
	Status      string `json:"status"`
	ContentType string `json:"content_type"`
	UploadedAt  string `json:"uploaded_at"`
	// UploadedBy wrote the current version.
	UploadedBy *Named `json:"uploaded_by"`
	Folder     *Named `json:"folder"`
	// Public is true when the file loads without a token: its folder, or a
	// folder above it, is Public.
	Public bool `json:"public"`
}

// FileVersion is one kept version of a file.
type FileVersion struct {
	// ID goes to AtVersion or Files.Restore.
	ID int64  `json:"id"`
	At string `json:"at"`
	// By is the xid of the user who wrote this version.
	By          string `json:"by"`
	Hash        string `json:"hash"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Current     bool   `json:"current"`
}

// FileDownload is a download in progress; close Body when done.
type FileDownload struct {
	Body        io.ReadCloser
	ContentType string
	// Length is the byte count of Body, -1 when the server did not say.
	Length int64
	// Hash is the ETag: the SHA-256 of the whole file.
	Hash string
	// Partial is true for a ByteRange answer.
	Partial bool
	// NotModified is true when IfNoneMatch still matched; Body is empty.
	NotModified bool
}

// FolderSettings are the values set on a folder itself; nil inherits.
type FolderSettings struct {
	Public  *bool `json:"public"`
	Cache   *int  `json:"cache"`
	CanLink *bool `json:"can_link"`
}

// Folder is a folder as the files endpoints answer it.
type Folder struct {
	XID  string `json:"xid"`
	Name string `json:"name"`
	Path string `json:"path"`
	// Parent is the parent folder xid, empty for ROOT.
	Parent string `json:"parent"`
	// Writable says whether you may put files here, create subfolders, rename and share.
	Writable bool `json:"writable"`
	// Public: files here load without a token; this folder's own value, else
	// the nearest folder above that has one.
	Public bool `json:"public"`
	// Cache is the seconds browsers and CDNs may keep the files, set the same
	// way; nil: they check every time.
	Cache *int `json:"cache"`
	// CanLink: download links may be made for files here, set the same way.
	CanLink bool `json:"can_link"`
	// Settings holds what is set on this folder itself.
	Settings FolderSettings `json:"settings"`
}

// FileLink is a download link: it loads the file without signing in.
type FileLink struct {
	XID string `json:"xid"`
	// File is the file's xid.
	File string `json:"file"`
	// CreatedBy made it; the link works while they can read the file.
	CreatedBy    string `json:"created_by"`
	ExpiresAt    string `json:"expires_at"`
	MaxDownloads *int   `json:"max_downloads"`
	Downloads    int    `json:"downloads"`
	Inline       bool   `json:"inline"`
	// URL is set only on the link Files.Link just made; it cannot be read back.
	URL string `json:"url,omitempty"`
}

// Listing is one folder's files and subfolders.
type Listing struct {
	Files   []FileInfo
	Folders []Folder
}

// SearchHit is one search result: a file or a folder.
type SearchHit struct {
	File   *FileInfo
	Folder *Folder
	// Path is the full path; empty for a file in no folder or in a folder you
	// cannot see.
	Path string
}

func (h *SearchHit) UnmarshalJSON(b []byte) error {
	var head struct {
		Type string  `json:"type"`
		Path *string `json:"path"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	if head.Path != nil {
		h.Path = *head.Path
	}
	if head.Type == "folder" {
		h.Folder = &Folder{}
		return json.Unmarshal(b, h.Folder)
	}
	h.File = &FileInfo{}
	return json.Unmarshal(b, h.File)
}

// SearchResults is one page of a search, shortest path first.
type SearchResults struct {
	Results []SearchHit `json:"results"`
	// Truncated says more results follow this page.
	Truncated bool `json:"truncated"`
}

// Grantees are the users and groups holding one access on a folder.
type Grantees struct {
	Users  []Named `json:"users"`
	Groups []Named `json:"groups"`
}

// FolderAccess is who holds read, write and delete on a folder.
type FolderAccess struct {
	Read   Grantees `json:"read"`
	Write  Grantees `json:"write"`
	Delete Grantees `json:"delete"`
}

func (f *FilesAPI) url(path string, q url.Values) string {
	u := f.c.endpoint + "/files" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func isPath(ref string) bool { return strings.HasPrefix(ref, "/") }

func isFolderPath(ref string) bool { return isPath(ref) && strings.HasSuffix(ref, "/") }

func xidPath(xid string) string { return "/" + url.PathEscape(xid) }

// fileAt is one file's endpoint: /files/<xid>, or /files?path= for a path.
func fileAt(ref, tail string) (string, url.Values) {
	if isPath(ref) {
		return tail, url.Values{"path": {ref}}
	}
	return xidPath(ref) + tail, nil
}

// folderAt is one folder's endpoint: /folders/<xid>, or /folders?path= for a path.
func folderAt(ref, tail string) (string, url.Values) {
	if isPath(ref) {
		return "/folders" + tail, url.Values{"path": {ref}}
	}
	return "/folders" + xidPath(ref) + tail, nil
}

// xidAt is the xid of a file named by path or xid; versions take only an xid.
func (f *FilesAPI) xidAt(ctx context.Context, ref string) (string, error) {
	if !isPath(ref) {
		return ref, nil
	}
	fi, err := f.Info(ctx, ref)
	if err != nil {
		return "", err
	}
	if fi == nil {
		e := newError("No such file: "+ref, "FILE_NOT_FOUND")
		e.Status = http.StatusNotFound
		return "", e
	}
	return fi.XID, nil
}

// send makes one /files request and maps a refusal to an *Error. The caller
// closes resp.Body on success.
func (f *FilesAPI) send(ctx context.Context, method, urlStr string, body io.Reader,
	headers map[string]string, timeout bool) (*http.Response, error) {

	resp, err := f.c.fetchAuthBody(ctx, method, urlStr, body, headers, timeout)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
	defer resp.Body.Close()
	rid := resp.Header.Get("X-Request-Id")
	raw := readBody(resp)
	se := parseServerError(raw)
	if (se != nil && se.Code == "NOT_FOUND") || (se == nil && resp.StatusCode == http.StatusNotFound) {
		e := newError("Files are not available on this server — is the files module running?", "FILES_UNAVAILABLE")
		e.Status, e.RequestID = resp.StatusCode, rid
		return nil, e
	}
	if se != nil {
		return nil, errorFromServer(se, resp.StatusCode, rid)
	}
	e := newError("Files request failed", "HTTP_ERROR")
	e.Status, e.RequestID, e.Details = resp.StatusCode, rid, string(raw)
	return nil, e
}

func (f *FilesAPI) json(ctx context.Context, method, path string, q url.Values, payload, out any) error {
	var body io.Reader
	headers := map[string]string{}
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return newError("failed to encode request: "+err.Error(), "INVALID_BODY")
		}
		body = bytes.NewReader(data)
		headers["Content-Type"] = "application/json"
	}
	resp, err := f.send(ctx, method, f.url(path, q), body, headers, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return newError("failed to decode files response: "+err.Error(), "INTERNAL_ERROR")
	}
	return nil
}

func (f *FilesAPI) write(ctx context.Context, method, path string, q url.Values, body io.Reader, o callOptions) (*FileInfo, error) {
	h := map[string]string{}
	if o.contentType != "" {
		h["Content-Type"] = o.contentType
	}
	if o.ifMatch != "" {
		h["If-Match"] = `"` + o.ifMatch + `"`
	}
	if strings.HasPrefix(strings.ToLower(o.contentType), "application/x-www-form-urlencoded") {
		return nil, newError("a form-encoded body is parsed as a form, not stored — send the bytes with their own type",
			"UNSUPPORTED_MEDIA_TYPE")
	}
	if body == nil {
		body = strings.NewReader("")
	}
	resp, err := f.send(ctx, method, f.url(path, q), body, h, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newError("failed to decode file: "+err.Error(), "INTERNAL_ERROR")
	}
	return &out, nil
}

// Put stores body. A path ("/a/b/name", the folder must exist) creates the
// file or replaces it; an xid replaces that file. Use ContentType and
// IfMatch. Body is read to the end and not closed; an io.Seeker body
// survives a token refresh mid-call.
func (f *FilesAPI) Put(ctx context.Context, target string, body io.Reader, opts ...Opt) (*FileInfo, error) {
	if target == "" {
		return nil, newError("put takes a path or an xid; PutNew makes a file in no folder", "INVALID_PATH")
	}
	path, q := fileAt(target, "")
	return f.write(ctx, http.MethodPut, path, q, body, applyOpts(opts))
}

// PutNew stores body as a new file called name in no folder; a second call
// makes another file.
func (f *FilesAPI) PutNew(ctx context.Context, name string, body io.Reader, opts ...Opt) (*FileInfo, error) {
	if name == "" {
		return nil, newError("PutNew needs a name", "NAME_REQUIRED")
	}
	return f.write(ctx, http.MethodPost, "", url.Values{"name": {name}}, body, applyOpts(opts))
}

func rangeHeader(r [2]int64) string {
	var start, end string
	if r[0] >= 0 {
		start = strconv.FormatInt(r[0], 10)
	}
	if r[1] >= 0 {
		end = strconv.FormatInt(r[1], 10)
	}
	return "bytes=" + start + "-" + end
}

// Download opens the bytes of a file. Use ByteRange, IfNoneMatch and
// AtVersion. Bound by ctx alone, not the client Timeout. The caller closes Body.
func (f *FilesAPI) Download(ctx context.Context, ref string, opts ...Opt) (*FileDownload, error) {
	o := applyOpts(opts)
	h := map[string]string{}
	if o.byteRange != nil {
		h["Range"] = rangeHeader(*o.byteRange)
	}
	if o.ifNoneMatch != "" {
		h["If-None-Match"] = `"` + o.ifNoneMatch + `"`
	}
	path, q := fileAt(ref, "")
	if o.version != nil {
		xid, err := f.xidAt(ctx, ref)
		if err != nil {
			return nil, err
		}
		path, q = xidPath(xid)+"/versions/"+strconv.FormatInt(*o.version, 10), nil
	}
	resp, err := f.send(ctx, http.MethodGet, f.url(path, q), nil, h, false)
	if err != nil {
		return nil, err
	}
	d := &FileDownload{
		Body:        resp.Body,
		ContentType: resp.Header.Get("Content-Type"),
		Length:      resp.ContentLength,
		Hash:        strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`),
		Partial:     resp.StatusCode == http.StatusPartialContent,
	}
	if resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		d.Body, d.Length, d.NotModified = http.NoBody, 0, true
		if d.Hash == "" {
			d.Hash = o.ifNoneMatch
		}
	}
	return d, nil
}

// Zip streams one zip of files and of everything inside folders (xids or
// paths), read as you; what you cannot read is listed in its _skipped.txt.
// Like every file transfer it is bound by ctx alone. The caller closes Body.
func (f *FilesAPI) Zip(ctx context.Context, files, folders []string) (*FileDownload, error) {
	if files == nil {
		files = []string{}
	}
	if folders == nil {
		folders = []string{}
	}
	raw, _ := json.Marshal(map[string][]string{"files": files, "folders": folders})
	resp, err := f.send(ctx, http.MethodPost, f.url("/zip", nil), bytes.NewReader(raw),
		map[string]string{"Content-Type": "application/json"}, false)
	if err != nil {
		return nil, err
	}
	return &FileDownload{Body: resp.Body, ContentType: resp.Header.Get("Content-Type"), Length: resp.ContentLength}, nil
}

// Info returns the file, or nil when it is not there or you cannot see it.
func (f *FilesAPI) Info(ctx context.Context, ref string) (*FileInfo, error) {
	var out FileInfo
	path, q := fileAt(ref, "/info")
	if err := f.json(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		if e, ok := err.(*Error); ok && (e.Code == "FILE_NOT_FOUND" || e.Code == "FOLDER_NOT_FOUND") {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// List returns a folder's files and subfolders: InFolder("/a/") for one
// folder, NoFolder() for the files in no folder, neither for every file you
// can see, newest first. Query keeps names holding its words in order;
// Limit (default 100, at most 1000) and Offset page the files.
func (f *FilesAPI) List(ctx context.Context, opts ...Opt) (*Listing, error) {
	o := applyOpts(opts)
	q := url.Values{}
	if o.fileQuery != "" {
		q.Set("q", o.fileQuery)
	}
	if o.limit != nil {
		q.Set("limit", strconv.Itoa(*o.limit))
	}
	if o.offset != nil {
		q.Set("offset", strconv.Itoa(*o.offset))
	}
	if o.noFolder {
		q.Set("folderless", "true")
	} else if o.folder != "" {
		q.Set("folder", o.folder)
	}
	var files struct {
		Files []FileInfo `json:"files"`
	}
	if err := f.json(ctx, http.MethodGet, "", q, nil, &files); err != nil {
		return nil, err
	}
	out := &Listing{Files: files.Files, Folders: []Folder{}}
	if o.noFolder || o.folder == "" {
		return out, nil
	}
	var folders struct {
		Folders []Folder `json:"folders"`
	}
	if err := f.json(ctx, http.MethodGet, "/folders", url.Values{"parent": {o.folder}}, nil, &folders); err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(o.fileQuery))
	for _, d := range folders.Folders {
		if holdsInOrder(strings.ToLower(d.Name), words) {
			out.Folders = append(out.Folders, d)
		}
	}
	return out, nil
}

func holdsInOrder(s string, words []string) bool {
	for _, w := range words {
		i := strings.Index(s, w)
		if i < 0 {
			return false
		}
		s = s[i+len(w):]
	}
	return true
}

// Search returns folders and files whose paths hold the words of q in order,
// shortest path first. InFolder keeps it below that folder; Limit and Offset page.
func (f *FilesAPI) Search(ctx context.Context, q string, opts ...Opt) (*SearchResults, error) {
	o := applyOpts(opts)
	v := url.Values{"q": {q}}
	if o.folder != "" {
		v.Set("folder", o.folder)
	}
	if o.limit != nil {
		v.Set("limit", strconv.Itoa(*o.limit))
	}
	if o.offset != nil {
		v.Set("offset", strconv.Itoa(*o.offset))
	}
	var out SearchResults
	if err := f.json(ctx, http.MethodGet, "/search", v, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deletes a file, or an empty folder (a path ending in /). A file's
// bytes are kept for the grace period.
func (f *FilesAPI) Delete(ctx context.Context, ref string) error {
	path, q := fileAt(ref, "")
	if isFolderPath(ref) {
		path, q = folderAt(ref, "")
	}
	return f.json(ctx, http.MethodDelete, path, q, nil, nil)
}

// encodePath percent-encodes each part of a path but for the RFC 3986
// unreserved characters.
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(url.QueryEscape(part), "+", "%20")
	}
	return strings.Join(parts, "/")
}

// URL returns where a file loads from, without a request: /files/<xid>, or
// /files/path/... for a path. Inline opens it in the browser instead of
// downloading it. Without a token it loads only from a Public folder.
func (f *FilesAPI) URL(ref string, opts ...Opt) string {
	u := f.c.endpoint + "/files" + xidPath(ref)
	if isPath(ref) {
		u = f.c.endpoint + "/files/path" + encodePath(ref)
	}
	if applyOpts(opts).inline {
		u += "?disposition=inline"
	}
	return u
}

// Move renames or moves. A file goes to to ("/a/new.png"), or into a folder
// when to ends in / (the name stays); a folder (from ending in /) moves to
// its new path. One of the answers is set: the file or the folder.
func (f *FilesAPI) Move(ctx context.Context, from, to string) (*FileInfo, *Folder, error) {
	if !isPath(to) {
		return nil, nil, newError("move takes the new path, for example /site/brand/logo.png", "INVALID_PATH")
	}
	if isFolderPath(from) {
		parts := strings.FieldsFunc(to, func(r rune) bool { return r == '/' })
		if len(parts) == 0 {
			return nil, nil, newError("a folder moves to a path like /archive/2026/", "INVALID_PATH")
		}
		parent := "/"
		if len(parts) > 1 {
			parent = "/" + strings.Join(parts[:len(parts)-1], "/") + "/"
		}
		var out Folder
		path, q := folderAt(from, "")
		if err := f.json(ctx, http.MethodPatch, path, q, map[string]any{"name": parts[len(parts)-1], "parent": parent}, &out); err != nil {
			return nil, nil, err
		}
		return nil, &out, nil
	}
	payload := map[string]any{"folder": to}
	if !strings.HasSuffix(to, "/") {
		cut := strings.LastIndex(to, "/")
		payload = map[string]any{"folder": to[:cut+1], "name": to[cut+1:]}
	}
	var out FileInfo
	path, q := fileAt(from, "")
	if err := f.json(ctx, http.MethodPatch, path, q, payload, &out); err != nil {
		return nil, nil, err
	}
	return &out, nil, nil
}

// Folder returns the folder at path, made with every missing folder above
// it. SetPublic, SetCache, SetCanLink and their Inherit forms change its settings.
func (f *FilesAPI) Folder(ctx context.Context, path string, opts ...Opt) (*Folder, error) {
	settings := applyOpts(opts).folderSettings
	if settings == nil {
		settings = map[string]any{}
	}
	var out Folder
	if err := f.json(ctx, http.MethodPut, "/folders", url.Values{"path": {path}}, settings, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (f *FilesAPI) share(ctx context.Context, folder, tail, access string, users, groups []string) error {
	if users == nil {
		users = []string{}
	}
	if groups == nil {
		groups = []string{}
	}
	path, q := folderAt(folder, tail)
	return f.json(ctx, http.MethodPost, path, q, map[string]any{"access": access, "users": users, "groups": groups}, nil)
}

// Share gives users and groups (xids) access ("read", "write" or "delete")
// on a folder; writers of the folder may.
func (f *FilesAPI) Share(ctx context.Context, folder, access string, users, groups []string) error {
	return f.share(ctx, folder, "/share", access, users, groups)
}

// Unshare takes access away from users and groups on a folder.
func (f *FilesAPI) Unshare(ctx context.Context, folder, access string, users, groups []string) error {
	return f.share(ctx, folder, "/unshare", access, users, groups)
}

// Access returns who holds read, write and delete on a folder.
func (f *FilesAPI) Access(ctx context.Context, folder string) (*FolderAccess, error) {
	var out FolderAccess
	path, q := folderAt(folder, "/access")
	if err := f.json(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Versions returns the kept versions of a file (the last ten), newest first.
func (f *FilesAPI) Versions(ctx context.Context, ref string) ([]FileVersion, error) {
	xid, err := f.xidAt(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out struct {
		Versions []FileVersion `json:"versions"`
	}
	if err := f.json(ctx, http.MethodGet, xidPath(xid)+"/versions", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Versions, nil
}

// Restore makes kept version id of a file current again; no bytes are copied.
func (f *FilesAPI) Restore(ctx context.Context, ref string, id int64) (*FileInfo, error) {
	xid, err := f.xidAt(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out FileInfo
	if err := f.json(ctx, http.MethodPost, xidPath(xid)+"/versions/"+strconv.FormatInt(id, 10)+"/restore", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Link makes a download link for a file in a folder where links are on. Use
// LinkExpires, LinkMaxDownloads and Inline. Its URL is shown only now.
func (f *FilesAPI) Link(ctx context.Context, ref string, opts ...Opt) (*FileLink, error) {
	xid, err := f.xidAt(ctx, ref)
	if err != nil {
		return nil, err
	}
	o := applyOpts(opts)
	payload := map[string]any{}
	if o.linkExpires != nil {
		payload["expires"] = *o.linkExpires
	}
	if o.linkMaxDl != nil {
		payload["max_downloads"] = *o.linkMaxDl
	}
	if o.inline {
		payload["inline"] = true
	}
	var out FileLink
	if err := f.json(ctx, http.MethodPost, xidPath(xid)+"/links", nil, payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Links returns the file's live links you can see: your own, or all of them
// for File Admin.
func (f *FilesAPI) Links(ctx context.Context, ref string) ([]FileLink, error) {
	xid, err := f.xidAt(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out struct {
		Links []FileLink `json:"links"`
	}
	if err := f.json(ctx, http.MethodGet, xidPath(xid)+"/links", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Links, nil
}

// Unlink ends a link (its xid) at once, on every engine.
func (f *FilesAPI) Unlink(ctx context.Context, link string) error {
	return f.json(ctx, http.MethodDelete, "/links"+xidPath(link), nil, nil, nil)
}

var servedHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag",
	"Content-Disposition", "Cache-Control", "X-Content-Type-Options", "Content-Security-Policy"}

// Serve answers a browser's request with a file. Range, If-None-Match and
// If-Range go through, and the answer keeps status, type, length, ETag and
// name; a refusal is the engine's own answer. Inline opens it in the browser.
// Our own credentials failing or the network is written as a plain 401/502:
// the caller never sees it, so check credentials at startup.
func (f *FilesAPI) Serve(w http.ResponseWriter, r *http.Request, ref string, opts ...Opt) {
	h := map[string]string{}
	for _, k := range []string{"Range", "If-None-Match", "If-Range"} {
		if v := r.Header.Get(k); v != "" {
			h[k] = v
		}
	}
	path, q := fileAt(ref, "")
	if applyOpts(opts).inline {
		if q == nil {
			q = url.Values{}
		}
		q.Set("disposition", "inline")
	}
	method := http.MethodGet
	if r.Method == http.MethodHead {
		method = http.MethodHead
	}
	resp, err := f.c.fetchAuthBody(r.Context(), method, f.url(path, q), nil, h, false)
	if err != nil {
		status := http.StatusBadGateway
		if e, ok := err.(*Error); ok && e.Status != 0 {
			status = e.Status
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer resp.Body.Close()
	for _, k := range servedHeaders {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}
