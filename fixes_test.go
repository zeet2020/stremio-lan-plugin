package main

import (
	"net/http/httptest"
	"testing"
)

// A negative skip used to panic the catalog handler.
func TestCatalogNegativeSkip(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/catalog/movie/lan-movies/skip=-5.json", nil)
	req.SetPathValue("id", "lan-movies")
	req.SetPathValue("extra", "skip=-5.json")
	handleCatalog(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

// Only http(s) DLNA resources are indexed: anything else would reach ffmpeg.
func TestDLNAItemScheme(t *testing.T) {
	srv := &Server{Name: "nas", UDN: "uuid:1"}
	for u, ok := range map[string]bool{
		"http://nas/MediaItems/1.mkv": true,
		"https://nas/a.mkv":           true,
		"file:///etc/passwd":          false,
		"concat:/a.mkv|/b.mkv":        false,
		"/etc/passwd":                 false,
	} {
		if got := dlnaItem(srv, &didlItem{Title: "x"}, &didlRes{URL: u}) != nil; got != ok {
			t.Errorf("dlnaItem(%q) indexed=%v, want %v", u, got, ok)
		}
	}
}

// The same key added twice (overlapping folders) is indexed once.
func TestAddDedupes(t *testing.T) {
	idx := newIndex()
	for range 2 {
		idx.add(&Item{Key: "a", Title: "Movie", FileName: "Movie.mkv", LocalFile: "/v/Movie.mkv"})
	}
	if len(idx.All) != 1 || len(idx.Movies) != 1 {
		t.Fatalf("all=%d movies=%d, want 1 and 1", len(idx.All), len(idx.Movies))
	}
}

// Reusing WebDAV folder /Movies must not pull in /Movies 2.
func TestReuseDAVFolderPrefix(t *testing.T) {
	prev := newIndex()
	prev.DAVStamps["b/Movies"] = "s1"
	prev.add(&Item{Key: "in", SourceID: "src", FileName: "A.mkv", URL: "http://h/Movies/A.mkv", DAVRoot: "http://h/"})
	prev.add(&Item{Key: "out", SourceID: "src", FileName: "B.mkv", URL: "http://h/Movies 2/B.mkv", DAVRoot: "http://h/"})
	idx := newIndex()
	idx.prev, idx.incremental, idx.cur = prev, true, "src"
	if !idx.reuseDAVFolder("b/Movies", "/Movies", "s1") {
		t.Fatal("folder not reused")
	}
	if idx.Items["in"] == nil || idx.Items["out"] != nil {
		t.Fatalf("reused %v, want only key in", idx.Items)
	}
}
