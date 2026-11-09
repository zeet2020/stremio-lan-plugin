package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuth(t *testing.T) {
	cfg = Config{}
	settings, settingsFile = Settings{}, ""
	initSecrets()
	h := routes()
	do := func(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	cookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookie {
				return c
			}
		}
		t.Fatalf("no session cookie (status %d)", rec.Code)
		return nil
	}
	tk := "/" + getSettings().AddonToken

	// Stremio side: token required.
	for path, want := range map[string]int{"/manifest.json": 404, "/0123abcd/manifest.json": 404, tk + "/manifest.json": 200} {
		if got := do("GET", path, "", nil).Code; got != want {
			t.Errorf("GET %s → %d, want %d", path, got, want)
		}
	}

	// UI side: login required.
	if got := do("GET", "/api/status", "", nil).Code; got != 401 {
		t.Errorf("status without login → %d", got)
	}
	if got := do("POST", "/rescan", "", nil).Code; got != 401 {
		t.Errorf("rescan without login → %d", got)
	}
	if body := do("GET", "/api/auth", "", nil).Body.String(); !strings.Contains(body, `"setup":true`) {
		t.Errorf("auth before setup: %s", body)
	}
	if got := do("POST", "/api/setup", `{"password":"short"}`, nil).Code; got != 400 {
		t.Errorf("short password → %d", got)
	}
	first := cookie(do("POST", "/api/setup", `{"password":"hunter22"}`, nil))
	if got := do("GET", "/api/status", "", first).Code; got != 200 {
		t.Errorf("status after setup → %d", got)
	}
	if got := do("POST", "/api/setup", `{"password":"takeover1"}`, nil).Code; got != 409 {
		t.Errorf("second setup → %d", got)
	}
	if got := do("POST", "/api/login", `{"password":"wrongpass"}`, nil).Code; got != 401 {
		t.Errorf("wrong login → %d", got)
	}
	cookie(do("POST", "/api/login", `{"password":"hunter22"}`, nil))
	forged := &http.Cookie{Name: sessionCookie, Value: "99999999999.AAAA"}
	if got := do("GET", "/api/status", "", forged).Code; got != 401 {
		t.Errorf("forged cookie → %d", got)
	}

	// Changing the password logs out other sessions.
	rec := do("POST", "/api/password", `{"current":"hunter22","new":"correct horse"}`, first)
	if rec.Code != 204 {
		t.Fatalf("change password → %d %s", rec.Code, rec.Body.String())
	}
	if got := do("GET", "/api/status", "", first).Code; got != 401 {
		t.Errorf("old session after password change → %d", got)
	}
	now := cookie(rec)
	if got := do("GET", "/api/status", "", now).Code; got != 200 {
		t.Errorf("new session → %d", got)
	}

	// Regenerating the token kills old install links.
	if got := do("POST", "/api/token", "", now).Code; got != 204 {
		t.Fatalf("rotate token → %d", got)
	}
	if got := do("GET", tk+"/manifest.json", "", nil).Code; got != 404 {
		t.Errorf("old token → %d", got)
	}
	if got := do("GET", "/"+getSettings().AddonToken+"/manifest.json", "", nil).Code; got != 200 {
		t.Errorf("new token → %d", got)
	}
}

// Wrong passwords are checked one at a time: parallel guesses don't go faster.
func TestLoginGuessesSerialized(t *testing.T) {
	cfg = Config{}
	settings, settingsFile = Settings{}, ""
	initSecrets()
	h := routes()
	req := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := req("/api/setup", `{"password":"correct horse"}`); rec.Code != 204 {
		t.Fatalf("setup → %d", rec.Code)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := req("/api/login", `{"password":"guess"}`); rec.Code != 401 {
				t.Errorf("wrong password → %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if took := time.Since(start); took < 3*time.Second {
		t.Errorf("3 parallel wrong guesses took %s; want ≥ 3s (one at a time)", took)
	}
}
