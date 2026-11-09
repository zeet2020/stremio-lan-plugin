package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenExternal(t *testing.T) {
	cfg = Config{}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	h := routes()
	link := `{"url":"stremio://192.168.0.101:7070/key/manifest.json"}`

	openExternal = nil // service build: no screen to open things on
	if code, _ := post(t, h, "/api/open", link); code != 404 {
		t.Errorf("service build → %d, want 404", code)
	}

	var opened []string
	openExternal = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openExternal = nil })
	if code, _ := post(t, h, "/api/open", link); code != 204 || len(opened) != 1 {
		t.Errorf("desktop → %d, opened %v", code, opened)
	}
	for _, bad := range []string{"https://evil.example", "file:///etc/passwd", "javascript:alert(1)"} {
		if code, _ := post(t, h, "/api/open", `{"url":"`+bad+`"}`); code != 400 {
			t.Errorf("open %s → %d, want 400", bad, code)
		}
	}
	rec := httptest.NewRecorder() // no admin cookie
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/open", strings.NewReader(link)))
	if rec.Code != 401 || len(opened) != 1 {
		t.Errorf("unauthenticated → %d", rec.Code)
	}
}
