package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// post sends an admin API request to h.
func post(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: newSession()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func drainJobs() {
	for len(jobs) > 0 {
		<-jobs
	}
}

func TestLocalFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Movies", ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, "Movies", "The.Matrix.1999.mkv"), []byte("0123456789"), 0o644)
	os.WriteFile(filepath.Join(dir, "Movies", "The.Matrix.1999.sample.mkv"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "Movies", ".hidden", "Secret.2001.mkv"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)

	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	current.Store(newIndex())
	h := routes()
	drainJobs()

	// explorer: list the folder (subfolders + video count), then a level down
	code, body := post(t, h, "/api/browse", `{"kind":"local","path":"`+dir+`"}`)
	if code != 200 || !strings.Contains(body, `"name":"Movies"`) || !strings.Contains(body, `"videos":0`) {
		t.Fatalf("browse → %d %s", code, body)
	}
	if code, body := post(t, h, "/api/browse", `{"kind":"local","path":"`+filepath.Join(dir, "Movies")+`"}`); code != 200 || !strings.Contains(body, `"videos":1`) {
		t.Fatalf("browse Movies → %d %s", code, body) // sample and hidden not counted
	}
	if code, body := post(t, h, "/api/browse", `{"kind":"local","path":""}`); code != 200 || !strings.Contains(body, `"path":"/tmp"`) {
		t.Errorf("browse top (/) → %d %s", code, body)
	}
	if code, _ := post(t, h, "/api/browse", `{"kind":"local","path":"relative"}`); code != 502 {
		t.Errorf("relative browse → %d", code)
	}

	// save: bad folders refused, a good one queues a scan of the host
	for _, bad := range []string{"relative/path", filepath.Join(dir, "nope"), filepath.Join(dir, "notes.txt")} {
		if code, _ := post(t, h, "/api/hosts", `{"kind":"local","folders":[{"path":"`+bad+`"}]}`); code != 400 {
			t.Errorf("save %q → %d, want 400", bad, code)
		}
	}
	if code, body := post(t, h, "/api/hosts", `{"kind":"local","folders":[{"path":"`+dir+`/","label":"media"}]}`); code != 204 {
		t.Fatalf("save → %d %s", code, body)
	}
	publish(rescanSource(<-jobs))
	idx := current.Load()
	if len(idx.All) != 1 || idx.All[0].Parsed.Title != "The Matrix" || len(idx.Sources) != 1 || idx.Sources[0].ID != "local:#"+dir {
		t.Fatalf("index: %d items, sources %+v", len(idx.All), idx.Sources)
	}
	tk := "/" + getSettings().AddonToken
	urls := streamURLs(t, h, tk+"/stream/movie/lan:m:"+idx.All[0].Key+".json")
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "http://example.com"+tk+"/file/") {
		t.Fatalf("stream: %v", urls)
	}
	rec := get(t, h, strings.TrimPrefix(urls[0], "http://example.com"), "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Errorf("local file: %d %q", rec.Code, rec.Body.String())
	}
	// a symlink swapped in after the scan is refused
	movie := filepath.Join(dir, "Movies", "The.Matrix.1999.mkv")
	os.Remove(movie)
	os.Symlink(filepath.Join(dir, "notes.txt"), movie)
	if rec := get(t, h, strings.TrimPrefix(urls[0], "http://example.com")); rec.Code != 404 {
		t.Errorf("symlinked file → %d, want 404", rec.Code)
	}
	if rec := get(t, h, tk+"/file/000000000000/notes.txt"); rec.Code != 404 {
		t.Errorf("unknown key → %d", rec.Code)
	}
}
