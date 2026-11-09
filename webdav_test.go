package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeDebrid is a WebDAV server with one folder per "torrent", each carrying
// an ETag, and a PROPFIND counter per path.
type fakeDebrid struct {
	mu    sync.Mutex
	dirs  map[string][]string // folder → video files
	etags map[string]string
	hits  map[string]int
}

func (f *fakeDebrid) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != "PROPFIND" {
		w.Write([]byte("video"))
		return
	}
	f.hits[r.URL.Path]++
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
	coll := func(href, etag string) {
		fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:getetag>%s</d:getetag></d:prop></d:propstat></d:response>`, href, etag)
	}
	if r.URL.Path == "/dav/" {
		coll("/dav/", `"root"`)
		for d := range f.dirs {
			coll("/dav/"+d+"/", f.etags[d])
		}
	} else {
		d := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dav/"), "/")
		coll(r.URL.Path, f.etags[d])
		for _, name := range f.dirs[d] {
			fmt.Fprintf(&b, `<d:response><d:href>/dav/%s/%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>10</d:getcontentlength></d:prop></d:propstat></d:response>`, d, name)
		}
	}
	b.WriteString(`</d:multistatus>`)
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(b.String()))
}

func (f *fakeDebrid) reset() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hits
	f.hits = map[string]int{}
	return h
}

func TestWebDAVIncremental(t *testing.T) {
	fake := &fakeDebrid{
		dirs:  map[string][]string{"Inception.2010": {"Inception.2010.mkv"}, "The.Matrix.1999": {"The.Matrix.1999.mkv"}},
		etags: map[string]string{"Inception.2010": `"a1"`, "The.Matrix.1999": `"b1"`},
		hits:  map[string]int{},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/dav/"
	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{Hosts: map[string]Host{
		"webdav:" + base: {Kind: "webdav", Addr: base, Folders: []Folder{{Path: "", Label: "/"}}},
	}}, "", nil
	current.Store(newIndex())

	movies := func() int { return len(current.Load().Movies) }
	scan := func(incremental bool) map[string]int {
		fake.reset()
		publish(build(context.Background(), incremental))
		return fake.reset()
	}

	if hits := scan(false); len(hits) != 3 || movies() != 2 {
		t.Fatalf("full scan: %v, %d movies", hits, movies())
	}
	// scheduled scan, nothing changed: only the top folder is listed
	if hits := scan(true); len(hits) != 1 || hits["/dav/"] != 1 || movies() != 2 {
		t.Fatalf("incremental, unchanged: %v, %d movies", hits, movies())
	}
	// a folder changes (new download inside): only it is reopened
	fake.mu.Lock()
	fake.dirs["The.Matrix.1999"] = append(fake.dirs["The.Matrix.1999"], "Blade.Runner.1982.mkv")
	fake.etags["The.Matrix.1999"] = `"b2"`
	fake.dirs["Alien.1979"] = []string{"Alien.1979.mkv"} // and a brand-new folder
	fake.etags["Alien.1979"] = `"c1"`
	fake.mu.Unlock()
	if hits := scan(true); len(hits) != 3 || hits["/dav/Inception.2010/"] != 0 || movies() != 4 {
		t.Fatalf("incremental, changed: %v, %d movies", hits, movies())
	}

	// the stamps survive a restart (library.db)
	path := filepath.Join(t.TempDir(), "library.db")
	if err := openDB(path); err != nil {
		t.Fatal(err)
	}
	saveIndex(current.Load())
	db.Close()
	db = nil
	current.Store(newIndex())
	loadLibrary(path)
	t.Cleanup(func() { db.Close(); db = nil })
	if hits := scan(true); len(hits) != 1 || movies() != 4 {
		t.Fatalf("incremental after reload: %v, %d movies", hits, movies())
	}

	// a user-triggered rescan always opens everything
	fake.reset()
	publish(rescanSource("webdav:" + base))
	if hits := fake.reset(); len(hits) != 4 || movies() != 4 {
		t.Fatalf("user rescan: %v, %d movies", hits, movies())
	}
}

// A WebDAV listing pointing at another server is ignored: scanning or playing
// it would send this server's login there.
func TestWebDAVOtherHostSkipped(t *testing.T) {
	var gotAuth sync.Map
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			gotAuth.Store(r.URL.Path, true)
		}
	}))
	defer other.Close()
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">
<d:response><d:href>/dav/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/dav/Here.2019.mkv</d:href><d:propstat><d:prop><d:getcontentlength>10</d:getcontentlength></d:prop></d:propstat></d:response>
<d:response><d:href>%[1]s/steal/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>%[1]s/steal/There.2019.mkv</d:href><d:propstat><d:prop><d:getcontentlength>10</d:getcontentlength></d:prop></d:propstat></d:response>
</d:multistatus>`, other.URL)
	}))
	defer dav.Close()

	u, _ := url.Parse(dav.URL + "/dav/")
	entries, err := propfind(u, Login{User: "me", Pass: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "Here.2019.mkv" {
		t.Errorf("entries %+v, want only the file on this server", entries)
	}
	gotAuth.Range(func(k, _ any) bool { t.Errorf("login sent to the other server: %v", k); return true })

	if sameServer(must(url.Parse(other.URL+"/x.mkv")), dav.URL+"/dav/") || !sameServer(must(url.Parse(dav.URL+"/dav/a.mkv")), dav.URL+"/dav/") {
		t.Error("sameServer wrong")
	}
	for a, want := range map[string]bool{
		"https://webdav.example/x":      true,
		"https://WebDAV.example:443/x":  true, // default port, any case
		"http://webdav.example/x":       false,
		"https://webdav.example:8443/x": false,
		"https://evil.example/x":        false,
	} {
		if got := sameServer(must(url.Parse(a)), "https://webdav.example/"); got != want {
			t.Errorf("sameServer(%s) = %v, want %v", a, got, want)
		}
	}
}
