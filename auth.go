package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Two kinds of client:
//   - the web UI: admin password → signed session cookie, required on /api/* and /rescan.
//   - Stremio and players: can't log in, so every add-on URL carries a secret
//     token: /{token}/manifest.json, /{token}/stream/..., /{token}/file/...

const sessionCookie = "stremio_lan_session"

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// initSecrets creates the add-on token and session key on first run.
func initSecrets() error {
	if s := getSettings(); s.AddonToken != "" && s.SessionKey != "" {
		return nil
	}
	return saveSettings(func(s *Settings) {
		if s.AddonToken == "" {
			s.AddonToken = randomHex(16)
		}
		if s.SessionKey == "" {
			s.SessionKey = randomHex(32)
		}
	})
}

// --- add-on token ---

// splitToken returns the path after a valid /{token} prefix.
func splitToken(path string) (string, bool) {
	tok, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	want := getSettings().AddonToken
	if want == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		return "", false
	}
	return "/" + rest, true
}

// addonBase is the URL Stremio installs from and stream links start with.
func addonBase(r *http.Request) string {
	return baseURL(r) + "/" + getSettings().AddonToken
}

// --- admin sessions ---

func sign(key, msg string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newSession returns a cookie value "expiry.hmac", valid 30 days. Changing the
// password rotates the key, which logs every session out.
func newSession() string {
	exp := strconv.FormatInt(time.Now().Add(30*24*time.Hour).Unix(), 10)
	return exp + "." + sign(getSettings().SessionKey, exp)
}

func authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, mac, ok := strings.Cut(c.Value, ".")
	t, err := strconv.ParseInt(exp, 10, 64)
	key := getSettings().SessionKey
	return ok && err == nil && key != "" && time.Now().Unix() < t && hmac.Equal([]byte(mac), []byte(sign(key, exp)))
}

func setSession(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

// requireAdmin wraps UI-only handlers.
func requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, obj{"setup": getSettings().PasswordHash == "", "authed": authed(r)}, false)
	})
	// First run only: whoever opens the UI first sets the admin password.
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Password string }
		if !readJSON(w, r, &req) || !validPassword(w, req.Password) {
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		taken := false
		if err := saveSettings(func(s *Settings) {
			if taken = s.PasswordHash != ""; !taken {
				s.PasswordHash = string(hash)
			}
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if taken {
			http.Error(w, "password already set", http.StatusConflict)
			return
		}
		setSession(w, r, newSession(), 30*24*3600)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Password string }
		if !readJSON(w, r, &req) {
			return
		}
		hash := getSettings().PasswordHash
		if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
			time.Sleep(time.Second) // slow down guessing; bcrypt adds more
			http.Error(w, "wrong password", http.StatusUnauthorized)
			return
		}
		setSession(w, r, newSession(), 30*24*3600)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		setSession(w, r, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/password", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Current, New string }
		if !readJSON(w, r, &req) || !validPassword(w, req.New) {
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(getSettings().PasswordHash), []byte(req.Current)) != nil {
			time.Sleep(time.Second)
			http.Error(w, "current password is wrong", http.StatusForbidden)
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
		if err := saveSettings(func(s *Settings) {
			s.PasswordHash = string(hash)
			s.SessionKey = randomHex(32)
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		setSession(w, r, newSession(), 30*24*3600) // keep this browser logged in
		w.WriteHeader(http.StatusNoContent)
	}))
	// New token: every Stremio install and stream link using the old one stops working.
	mux.HandleFunc("POST /api/token", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		saved(w, saveSettings(func(s *Settings) { s.AddonToken = randomHex(16) }))
	}))
}

func validPassword(w http.ResponseWriter, p string) bool {
	if len(p) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return false
	}
	return true
}
