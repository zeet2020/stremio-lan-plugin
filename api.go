package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// --- settings (what the web UI changes), persisted as JSON ---

type Login struct {
	User    string `json:"user,omitempty"`
	Pass    string `json:"pass,omitempty"`
	HostKey string `json:"hostKey,omitempty"` // SFTP: trusted server key, base64 wire format
}

type Settings struct {
	Hosts map[string]Host `json:"hosts,omitempty"` // host ID → connection + chosen folders

	// Old per-server model, read once and converted by migrateSettings.
	Disabled map[string]bool  `json:"disabled,omitempty"`
	Allowed  map[string]bool  `json:"allowed,omitempty"`
	DLNA     []string         `json:"dlna,omitempty"`
	SMB      map[string]Login `json:"smb,omitempty"`
	WebDAV   map[string]Login `json:"webdav,omitempty"`
	SFTP     map[string]Login `json:"sftp,omitempty"`
	Local    []string         `json:"local,omitempty"`

	PasswordHash string `json:"passwordHash,omitempty"` // admin, bcrypt; delete it to reset the password
	SessionKey   string `json:"sessionKey,omitempty"`   // signs UI session cookies
	AddonToken   string `json:"addonToken,omitempty"`   // secret path prefix for Stremio URLs

	Thumbnails bool   `json:"thumbnails,omitempty"` // grab a frame of each video after scans (thumbs.go)
	ThumbDir   string `json:"thumbDir,omitempty"`   // where thumbnails go; "" = thumbs/ next to this file

	Desktop DesktopPrefs `json:"desktop,omitempty"` // desktop app only
}

// DesktopPrefs: how the desktop app behaves with its window and the tray.
// Zero values are the defaults: close to tray, start with a window, no autostart.
type DesktopPrefs struct {
	QuitOnClose bool `json:"quitOnClose,omitempty"` // closing the window quits instead of hiding to the tray
	StartHidden bool `json:"startHidden,omitempty"` // start in the tray, no window
	Autostart   bool `json:"autostart,omitempty"`   // start at login (hidden in the tray)
}

var (
	settingsMu   sync.Mutex
	settings     Settings
	settingsFile string // empty: don't persist (tests)
)

func getSettings() Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return Settings{
		Hosts: maps.Clone(settings.Hosts), // Host values are replaced, never mutated in place

		PasswordHash: settings.PasswordHash,
		SessionKey:   settings.SessionKey,
		AddonToken:   settings.AddonToken,
		Thumbnails:   settings.Thumbnails,
		ThumbDir:     settings.ThumbDir,
		Desktop:      settings.Desktop,
	}
}

func loadSettings() {
	home, _ := os.UserHomeDir()
	settingsFile = env("SETTINGS_FILE", filepath.Join(home, ".stremio-lan", "settings.json"))
	b, err := os.ReadFile(settingsFile)
	if err == nil {
		err = json.Unmarshal(b, &settings)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err == nil && migrateSettings(&settings) {
		log.Printf("settings: converted sources to the explorer model (folders to scan)")
		err = saveSettings(func(*Settings) {})
	}
	if err == nil {
		err = initSecrets()
	}
	if err != nil {
		log.Fatalf("settings %s: %v", settingsFile, err)
	}
}

// saveSettings applies f and saves (0600: it holds passwords and secrets).
func saveSettings(f func(*Settings)) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if settings.Hosts == nil {
		settings.Hosts = map[string]Host{}
	}
	f(&settings)
	if settingsFile == "" {
		return nil
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.MkdirAll(filepath.Dir(settingsFile), 0o700); err != nil {
		return err
	}
	// Synced before the rename: a power cut (a Pi's SD card) leaves the old
	// file or the new one, never an empty one the service can't start with.
	tmp := settingsFile + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = out.Write(b); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, settingsFile)
}

// --- JSON API for the web UI ---

//go:embed all:web/dist
var webDist embed.FS

func apiRoutes(mux *http.ServeMux) {
	ui, _ := fs.Sub(webDist, "web/dist")
	mux.Handle("GET /", http.FileServerFS(ui))
	authRoutes(mux)
	mux.HandleFunc("GET /api/status", requireAdmin(handleAPIStatus))
	mux.HandleFunc("POST /rescan", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		queueRescan()
		w.WriteHeader(http.StatusAccepted)
	}))
	mux.HandleFunc("GET /api/library", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, obj{"thumbnails": thumbsOn(), "tool": thumbTool(), "thumbCount": thumbCount(),
			"thumbDir": thumbDir(), "thumbDirDefault": defaultThumbDir()}, false)
	}))
	// Either field may be sent alone; thumbDir "" goes back to the default.
	mux.HandleFunc("POST /api/library", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Thumbnails *bool
			ThumbDir   *string
		}
		if !readJSON(w, r, &p) {
			return
		}
		if p.ThumbDir != nil {
			d, err := validThumbDir(*p.ThumbDir)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			*p.ThumbDir = d
		}
		err := saveSettings(func(s *Settings) {
			if p.Thumbnails != nil {
				s.Thumbnails = *p.Thumbnails
			}
			if p.ThumbDir != nil {
				s.ThumbDir = *p.ThumbDir
			}
		})
		if err == nil {
			genThumbs()
		}
		saved(w, err)
	}))
	mux.HandleFunc("POST /api/library/clear-thumbs", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		err := clearThumbs()
		if errors.Is(err, errThumbsBusy) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		saved(w, err)
	}))
	hostRoutes(mux)
	if desktopRoutes != nil {
		desktopRoutes(mux)
	}
}

// validAddr checks and normalizes a manually added source address.
func validAddr(kind, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	switch kind {
	case "dlna", "webdav":
		u, err := url.Parse(addr)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", errors.New("need an http:// or https:// URL")
		}
		if kind == "webdav" && !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		return u.String(), nil
	case "local":
		return "", nil
	case "sftp":
		if !strings.Contains(addr, "://") {
			addr = "sftp://" + addr
		}
		u, err := url.Parse(addr)
		if err != nil || (u.Scheme != "sftp" && u.Scheme != "ssh") || u.Hostname() == "" || u.User != nil {
			return "", errors.New("need a host or host:port (put the user in the User field)")
		}
		return "sftp://" + u.Host, nil // folders are picked in the explorer
	case "smb":
		addr = strings.TrimPrefix(addr, "smb://")
		if addr == "" || strings.ContainsAny(addr, "/\\ ") {
			return "", errors.New("need a host or host:port")
		}
		return addr, nil
	}
	return "", errors.New("kind must be dlna, smb, webdav, sftp or local")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func saved(w http.ResponseWriter, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	idx := current.Load()
	st := getSettings()
	byFolder := map[string]Source{}
	for _, src := range idx.Sources {
		byFolder[src.ID] = src
	}
	type folderView struct {
		Source
		Scanned bool     `json:"scanned"` // false until its first scan finishes
		Kinds   []string `json:"kinds"`
	}
	type hostView struct {
		ID      string       `json:"id"`
		Kind    string       `json:"kind"`
		Addr    string       `json:"addr"`
		Name    string       `json:"name"`
		User    string       `json:"user,omitempty"`
		Folders []folderView `json:"folders"`
	}
	hosts := []hostView{}
	configured := map[string]bool{}
	for _, h := range st.hostList() {
		id := hostID(h.Kind, h.Addr)
		configured[id] = true
		v := hostView{ID: id, Kind: h.Kind, Addr: h.Addr, Name: h.Name, User: h.User, Folders: []folderView{}}
		if v.Name == "" {
			v.Name = map[bool]string{true: "This machine", false: h.Addr}[h.Kind == "local"]
		}
		for _, f := range h.Folders {
			fid := folderID(id, f.Path)
			src, ok := byFolder[fid]
			if !ok {
				src = Source{ID: fid, Host: id, Kind: h.Kind, Name: f.Label, Addr: f.Path}
			}
			v.Folders = append(v.Folders, folderView{src, ok, f.kindList()})
		}
		hosts = append(hosts, v)
	}
	found := []Discovered{}
	for _, d := range idx.Discovered {
		if !configured[d.ID] {
			found = append(found, d)
		}
	}
	resp := obj{
		"scanning":    scanning.Load(),
		"scanningId":  scanningID.Load(),
		"items":       len(idx.All),
		"movies":      len(idx.Movies),
		"shows":       len(idx.Shows),
		"hosts":       hosts,
		"discovered":  found,
		"manifestUrl": shareBase(r) + "/" + st.AddonToken + "/manifest.json",
		"installUrl":  "stremio://" + strings.TrimPrefix(strings.TrimPrefix(shareBase(r), "https://"), "http://") + "/" + st.AddonToken + "/manifest.json",
	}
	if !idx.Built.IsZero() {
		resp["built"] = idx.Built
	}
	writeJSON(w, resp, false)
}

// sameOrigin rejects cross-site POSTs so a web page can't change settings
// through a logged-in admin's browser (on top of the SameSite cookie).
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}
