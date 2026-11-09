package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port, HTTPSPort int
	Bind            string
	Servers         []string
	SMBServers      []string
	LocalDirs       []string // LOCAL_DIRS: folders to scan, in addition to ones added in the UI
	SMBDiscover     bool
	DLNADiscover    bool
	SMBUser         string
	SMBPass         string
	RootID          string
	Rescan          time.Duration
	MaxDepth        int
	MatchIMDB       bool
	Proxy           bool
	PublicURL       string
	Debug           bool
}

var cfg Config

// version is stamped at build time: -ldflags "-X main.version=1.2.3".
var version = "0.1.0"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	n, err := strconv.Atoi(env(k, strconv.Itoa(def)))
	if err != nil {
		log.Fatalf("bad %s: %v", k, err)
	}
	return n
}

func loadConfig() Config {
	c := Config{
		Port:      envInt("PORT", 7070),
		HTTPSPort: envInt("HTTPS_PORT", 0),
		Bind:      env("BIND_ADDRESS", "0.0.0.0"),
		RootID:    env("ROOT_OBJECT_ID", "0"),
		Rescan:    time.Duration(envInt("RESCAN_MINUTES", 30)) * time.Minute,
		MaxDepth:  envInt("MAX_DEPTH", 12),
		MatchIMDB: env("MATCH_IMDB", "1") == "1",
		Proxy:     env("PROXY_STREAMS", "0") == "1",
		PublicURL: strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		Debug:     env("LOG_LEVEL", "info") == "debug",
	}
	c.Servers = list(env("DLNA_SERVERS", ""))
	c.SMBServers = list(env("SMB_SERVERS", ""))
	c.LocalDirs = list(env("LOCAL_DIRS", ""))
	c.SMBDiscover = env("SMB_DISCOVER", "1") == "1"
	c.DLNADiscover = env("DLNA_DISCOVER", "1") == "1"
	c.SMBUser, c.SMBPass = env("SMB_USER", ""), env("SMB_PASS", "")
	return c
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func debugf(format string, a ...any) {
	if cfg.Debug {
		log.Printf("debug: "+format, a...)
	}
}

// openExternal opens a link with the OS (desktop app only; nil in the
// service, which runs on a server with no one at its screen).
var openExternal func(url string) error

// desktopRoutes adds the desktop app's own API (its tray/autostart options).
var desktopRoutes func(*http.ServeMux)

// setup loads env config and the saved settings. Both entry points call it first.
func setup() {
	cfg = loadConfig()
	loadSettings()
	loadLibrary(filepath.Join(filepath.Dir(settingsFile), "library.db"))
}

// selfURL is how this machine reaches its own HTTP server.
func selfURL() string {
	host := "127.0.0.1"
	if ip := net.ParseIP(cfg.Bind); ip != nil && !ip.IsUnspecified() {
		host = cfg.Bind
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Port))
}

// newServer limits how long a client may take to send its request headers
// and to sit idle between requests. No write timeout: a stream runs for hours.
func newServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

// serve starts scanning and the HTTP server(s), returning the control panel URL.
// If the port can't be bound (e.g. the service already runs on this machine),
// it returns the error with the URL of whatever is listening there.
func serve() (string, error) {
	panel := selfURL() + "/"
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return panel, err
	}
	go scheduler()
	genThumbs() // finish thumbnails a restart interrupted
	h := routes()
	if cfg.HTTPSPort > 0 {
		go serveTLS(h)
	}
	log.Printf("listening on http://%s", ln.Addr())
	go func() { log.Fatal(newServer(h).Serve(ln)) }()
	return panel, nil
}
