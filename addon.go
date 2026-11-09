package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// routes: Stremio-facing endpoints live under /{token}/; everything else is
// the web UI, its login and /health.
func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /manifest.json", handleManifest)
	mux.HandleFunc("GET /catalog/{type}/{id}", handleCatalog)
	mux.HandleFunc("GET /catalog/{type}/{id}/{extra}", handleCatalog)
	mux.HandleFunc("GET /meta/{type}/{id}", handleMeta)
	mux.HandleFunc("GET /stream/{type}/{id}", handleStream)
	mux.HandleFunc("GET /proxy/{key}", handleProxy) // GET pattern also matches HEAD
	mux.HandleFunc("GET /file/{key}/{name}", handleFile)
	mux.HandleFunc("GET /thumb/{file}", handleThumb)
	mux.HandleFunc("GET /poster.png", handlePoster)

	ui := http.NewServeMux()
	ui.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	apiRoutes(ui)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if _, ok := splitToken(r.URL.Path); !ok {
			ui.ServeHTTP(w, r)
			return
		}
		// Stremio (web too) needs CORS; the UI API deliberately doesn't get it.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.StripPrefix("/"+getSettings().AddonToken, mux).ServeHTTP(w, r)
	})
	if cfg.Debug {
		return logRequests(h)
	}
	return h
}

// logRequests logs each request (LOG_LEVEL=debug / -v) with the secret add-on
// key masked, so logs are safe to paste.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		p := r.URL.Path
		if tok := getSettings().AddonToken; tok != "" {
			p = strings.Replace(p, "/"+tok, "/<key>", 1)
		}
		extra := ""
		if rg := r.Header.Get("Range"); rg != "" {
			extra = " range=" + rg
		}
		log.Printf("http %s %s → %d %dB %s from %s%s", r.Method, p, rec.status, rec.bytes,
			time.Since(start).Round(time.Millisecond), r.RemoteAddr, extra)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach Flush on the real writer (the
// stream proxy flushes as it goes).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func writeJSON(w http.ResponseWriter, v any, cache bool) {
	w.Header().Set("Content-Type", "application/json")
	if cache {
		w.Header().Set("Cache-Control", "max-age=60")
	}
	json.NewEncoder(w).Encode(v)
}

// pathID returns a route segment with the ".json" suffix removed.
func pathID(r *http.Request, name string) string {
	return strings.TrimSuffix(r.PathValue(name), ".json")
}

type obj = map[string]any

func handleManifest(w http.ResponseWriter, r *http.Request) {
	streamPrefixes := []string{"lan:"}
	if cfg.MatchIMDB {
		streamPrefixes = append(streamPrefixes, "tt")
	}
	extra := []obj{{"name": "search"}, {"name": "skip"}}
	writeJSON(w, obj{
		"id":          "community.stremio-lan",
		"version":     version,
		"name":        "Stremio LAN Library",
		"description": "Videos, music, torrents and playlists from DLNA, SMB, WebDAV and SFTP servers and folders on your network",
		"resources": []any{
			"catalog",
			obj{"name": "meta", "types": []string{"movie", "series", "other"}, "idPrefixes": []string{"lan:"}},
			obj{"name": "stream", "types": []string{"movie", "series", "other"}, "idPrefixes": streamPrefixes},
		},
		"types": []string{"movie", "series", "other"},
		"catalogs": []obj{
			{"type": "movie", "id": "lan-movies", "name": "LAN Movies", "extra": extra},
			{"type": "series", "id": "lan-series", "name": "LAN Series", "extra": extra},
			{"type": "other", "id": "lan-audio", "name": "LAN Audio", "extra": extra},
			{"type": "other", "id": "lan-playlists", "name": "LAN Playlists", "extra": extra},
		},
		"behaviorHints": obj{"configurable": false},
	}, false)
}

// setPoster uses the server's poster, else a generated thumbnail (a 16:9
// frame, so shown landscape rather than cropped to a portrait poster), else a
// drawn poster with the title in the meta's shape.
func setPoster(m obj, base, poster, thumb, label string) {
	switch {
	case poster != "":
		m["poster"] = poster
	case thumb != "":
		m["poster"], m["posterShape"], m["background"] = thumb, "landscape", thumb
	default:
		m["poster"] = fallbackURL(base, m["posterShape"].(string), m["name"].(string), label)
	}
}

func movieMeta(idx *Index, base string, it *Item) obj {
	m := obj{"id": "lan:m:" + it.Key, "type": "movie", "name": idx.movieName(it), "posterShape": "poster"}
	if it.Parsed.Year > 0 {
		m["releaseInfo"] = strconv.Itoa(it.Parsed.Year)
	}
	label := "Movie"
	if it.Parsed.Year > 0 {
		label = strconv.Itoa(it.Parsed.Year)
	}
	setPoster(m, base, it.Poster, thumbURL(base, it), label)
	return m
}

func albumMeta(base string, al *Album) obj {
	m := obj{"id": "lan:a:" + al.Key, "type": "other", "name": al.Name, "posterShape": "square",
		"description": fmt.Sprintf("%d tracks · %s", len(al.Tracks), al.Tracks[0].Server)}
	poster := ""
	if i := slices.IndexFunc(al.Tracks, func(t *Item) bool { return t.Poster != "" }); i >= 0 {
		poster = al.Tracks[i].Poster
	}
	setPoster(m, base, poster, "", "Album")
	return m
}

func playlistMeta(base string, pl *Item) obj {
	m := obj{"id": "lan:p:" + pl.Key, "type": "other", "name": pl.Title, "posterShape": "square",
		"description": fmt.Sprintf("Playlist · %d entries · %s", len(pl.Entries), pl.Server)}
	setPoster(m, base, "", "", "Playlist")
	return m
}

func showPoster(sh *Show) string {
	for _, eps := range sh.Episodes {
		for _, it := range eps {
			if it.Poster != "" {
				return it.Poster
			}
		}
	}
	return ""
}

// episodeKeys is the show's (season, episode) pairs in order.
func episodeKeys(sh *Show) [][2]int {
	keys := make([][2]int, 0, len(sh.Episodes))
	for k := range sh.Episodes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	return keys
}

// episodeThumb is the generated thumbnail of an episode's first file.
func episodeThumb(base string, sh *Show, k [2]int) string {
	if eps := sh.Episodes[k]; len(eps) > 0 {
		return thumbURL(base, eps[0])
	}
	return ""
}

func showMeta(base string, sh *Show) obj {
	m := obj{"id": "lan:s:" + sh.Slug, "type": "series", "name": sh.Name, "posterShape": "poster"}
	thumb := ""
	if keys := episodeKeys(sh); len(keys) > 0 {
		thumb = episodeThumb(base, sh, keys[0])
	}
	setPoster(m, base, showPoster(sh), thumb, "Series")
	return m
}

func handleCatalog(w http.ResponseWriter, r *http.Request) {
	idx := current.Load()
	base := addonBase(r)
	q, _ := url.ParseQuery(pathID(r, "extra"))
	search := strings.ToLower(q.Get("search"))
	skip, _ := strconv.Atoi(q.Get("skip"))

	metas := []obj{}
	match := func(name string) bool { return search == "" || strings.Contains(strings.ToLower(name), search) }
	switch pathID(r, "id") {
	case "lan-movies":
		for _, it := range idx.Movies {
			if match(it.Parsed.Title) || match(it.Title) {
				metas = append(metas, movieMeta(idx, base, it))
			}
		}
	case "lan-series":
		shows := make([]*Show, 0, len(idx.Shows))
		for _, sh := range idx.Shows {
			shows = append(shows, sh)
		}
		sort.Slice(shows, func(i, j int) bool { return strings.ToLower(shows[i].Name) < strings.ToLower(shows[j].Name) })
		for _, sh := range shows {
			if match(sh.Name) {
				metas = append(metas, showMeta(base, sh))
			}
		}
	case "lan-audio":
		albums := make([]*Album, 0, len(idx.Albums))
		for _, al := range idx.Albums {
			albums = append(albums, al)
		}
		sort.Slice(albums, func(i, j int) bool { return strings.ToLower(albums[i].Name) < strings.ToLower(albums[j].Name) })
		for _, al := range albums {
			if match(al.Name) || slices.ContainsFunc(al.Tracks, func(t *Item) bool { return match(t.Title) }) {
				metas = append(metas, albumMeta(base, al))
			}
		}
	case "lan-playlists":
		for _, pl := range idx.Playlists {
			if match(pl.Title) {
				metas = append(metas, playlistMeta(base, pl))
			}
		}
	}
	if skip > len(metas) {
		skip = len(metas)
	}
	metas = metas[skip:]
	if len(metas) > 100 {
		metas = metas[:100]
	}
	writeJSON(w, obj{"metas": metas}, true)
}

func sizeGB(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

func handleMeta(w http.ResponseWriter, r *http.Request) {
	idx := current.Load()
	base := addonBase(r)
	id := pathID(r, "id")
	switch {
	case strings.HasPrefix(id, "lan:m:"):
		if it := idx.Items[strings.TrimPrefix(id, "lan:m:")]; it != nil {
			m := movieMeta(idx, base, it)
			parts := []string{it.Server}
			if f := it.folder(); f != "" {
				parts = append(parts, "📁 "+f)
			}
			if it.Resolution != "" {
				parts = append(parts, it.Resolution)
			}
			if it.Size > 0 {
				parts = append(parts, sizeGB(it.Size))
			}
			m["description"] = strings.Join(parts, " · ")
			writeJSON(w, obj{"meta": m}, true)
			return
		}
	case strings.HasPrefix(id, "lan:a:"):
		if al := idx.Albums[strings.TrimPrefix(id, "lan:a:")]; al != nil {
			m := albumMeta(base, al)
			videos := []obj{}
			for i, t := range al.Tracks {
				videos = append(videos, obj{"id": id + ":" + t.Key, "title": t.Title, "episode": i + 1, "released": "1970-01-01T00:00:00.000Z"})
			}
			m["videos"] = videos
			writeJSON(w, obj{"meta": m}, true)
			return
		}
	case strings.HasPrefix(id, "lan:p:"):
		if pl := idx.Items[strings.TrimPrefix(id, "lan:p:")]; pl != nil && pl.Kind == "playlist" {
			m := playlistMeta(base, pl)
			videos := []obj{}
			for i, e := range pl.Entries {
				t := e.Title
				if t == "" {
					t = path.Base(e.Ref)
				}
				videos = append(videos, obj{"id": id + ":" + strconv.Itoa(i), "title": t, "episode": i + 1, "released": "1970-01-01T00:00:00.000Z"})
			}
			m["videos"] = videos
			writeJSON(w, obj{"meta": m}, true)
			return
		}
	case strings.HasPrefix(id, "lan:s:"):
		if sh := idx.Shows[strings.TrimPrefix(id, "lan:s:")]; sh != nil {
			m := showMeta(base, sh)
			videos := []obj{}
			for _, k := range episodeKeys(sh) {
				v := obj{
					"id":       fmt.Sprintf("lan:s:%s:%d:%d", sh.Slug, k[0], k[1]),
					"title":    fmt.Sprintf("S%02dE%02d", k[0], k[1]),
					"season":   k[0],
					"episode":  k[1],
					"released": "1970-01-01T00:00:00.000Z",
				}
				if t := episodeThumb(base, sh, k); t != "" {
					v["thumbnail"] = t
				}
				videos = append(videos, v)
			}
			m["videos"] = videos
			writeJSON(w, obj{"meta": m}, true)
			return
		}
	}
	writeJSON(w, obj{"meta": nil}, false)
}

// findStreams resolves a stream id to items. Never fails: unknown → nil.
func findStreams(idx *Index, typ, id string) []*Item {
	parts := strings.Split(id, ":")
	switch {
	case strings.HasPrefix(id, "lan:m:"):
		if it := idx.Items[strings.TrimPrefix(id, "lan:m:")]; it != nil {
			return []*Item{it}
		}
	case strings.HasPrefix(id, "lan:a:") && len(parts) == 4:
		if it := idx.Items[parts[3]]; it != nil && it.Kind == "audio" {
			return []*Item{it}
		}
	case strings.HasPrefix(id, "lan:s:") && len(parts) == 5:
		se, _ := strconv.Atoi(parts[3])
		ep, _ := strconv.Atoi(parts[4])
		if sh := idx.Shows[parts[2]]; sh != nil {
			return sh.Episodes[[2]int{se, ep}]
		}
	case cfg.MatchIMDB && strings.HasPrefix(id, "tt"):
		if typ == "series" && len(parts) == 3 {
			se, _ := strconv.Atoi(parts[1])
			ep, _ := strconv.Atoi(parts[2])
			if c, ok := cinemeta("series", parts[0]); ok {
				return idx.matchEpisode(c.Name, se, ep)
			}
		} else if typ == "movie" && len(parts) == 1 {
			if c, ok := cinemeta("movie", id); ok {
				return idx.matchMovie(c.Name, c.Year)
			}
		}
	}
	return nil
}

func baseURL(r *http.Request) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// shareHost is the host:port to put in links meant for other devices (the
// install link). Opened as localhost/127.0.0.1 (a browser on this machine,
// or the desktop app's window), that would only work here and Stremio
// syncs it to every device, so swap in this machine's LAN address.
func shareHost(reqHost string) string {
	host, port, err := net.SplitHostPort(reqHost)
	if err != nil {
		host, port = reqHost, ""
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return reqHost
	}
	lan := lanIP()
	if lan == "" {
		return reqHost
	}
	if port == "" {
		return lan
	}
	return net.JoinHostPort(lan, port)
}

// lanIP is the address other devices reach this machine on: BIND_ADDRESS if
// it names one, else the interface the default route uses.
func lanIP() string {
	if ip := net.ParseIP(cfg.Bind); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		return cfg.Bind
	}
	// UDP "dial" sends nothing; it just picks the outgoing interface.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			return n.IP.String()
		}
	}
	return ""
}

// shareBase is baseURL for links other devices will use.
func shareBase(r *http.Request) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + shareHost(r.Host)
}

func handleStream(w http.ResponseWriter, r *http.Request) {
	idx := current.Load()
	id := pathID(r, "id")
	streams := []obj{}
	for _, it := range findStreams(idx, r.PathValue("type"), id) {
		streams = append(streams, itemStream(r, it))
	}
	// playlist entry: lan:p:<playlist>:<n> → an indexed file or a URL
	if parts := strings.Split(id, ":"); strings.HasPrefix(id, "lan:p:") && len(parts) == 4 {
		n, _ := strconv.Atoi(parts[3])
		if pl := idx.Items[parts[2]]; pl != nil && n >= 0 && n < len(pl.Entries) {
			e := pl.Entries[n]
			if u, it := idx.resolveEntry(pl, e); it != nil {
				streams = append(streams, itemStream(r, it))
			} else if u != "" {
				t := e.Title
				if t == "" {
					t = u
				}
				streams = append(streams, obj{"url": u, "name": "LAN\nPlaylist", "title": t, "behaviorHints": obj{"notWebReady": true}})
			}
		}
	}
	writeJSON(w, obj{"streams": streams}, false)
}

// itemStream is a Stremio stream for an indexed file: an http URL (LAN), or
// infoHash/fileIdx for a video inside a .torrent (Stremio's torrent engine).
func itemStream(r *http.Request, it *Item) obj {
	info := []string{}
	if it.Size > 0 {
		info = append(info, "💾 "+sizeGB(it.Size))
	}
	if it.Resolution != "" {
		info = append(info, it.Resolution)
	}
	title := it.DisplayName()
	if f := it.folder(); f != "" {
		title += "\n📁 " + f
	}
	if len(info) > 0 {
		title += "\n" + strings.Join(info, " · ")
	}
	hints := obj{"notWebReady": true, "filename": it.DisplayName()}
	if it.Parsed.Season > 0 {
		hints["bingeGroup"] = "lan-" + normalize(it.Parsed.Title)
	}
	if it.Size > 0 {
		hints["videoSize"] = it.Size
	}
	if it.InfoHash != "" {
		var sources []string
		for _, t := range it.Trackers {
			sources = append(sources, "tracker:"+t)
		}
		st := obj{"infoHash": it.InfoHash, "fileIdx": it.FileIdx, "name": "LAN torrent\n" + it.Server, "title": title, "behaviorHints": hints}
		if len(sources) > 0 {
			st["sources"] = append(sources, "dht:"+it.InfoHash)
		}
		return st
	}
	u := it.URL
	switch {
	case it.SMBHost != "" || it.DAVRoot != "" || it.SFTPAddr != "" || it.LocalFile != "":
		u = addonBase(r) + "/file/" + it.Key + "/" + url.PathEscape(it.FileName)
	case cfg.Proxy:
		u = addonBase(r) + "/proxy/" + it.Key
	}
	return obj{"url": u, "name": "LAN\n" + it.Server, "title": title, "behaviorHints": hints}
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	it := current.Load().Items[r.PathValue("key")]
	if it == nil || it.URL == "" {
		http.NotFound(w, r)
		return
	}
	proxyTo(w, r, it, Login{})
}

// handleFile serves SMB, WebDAV, SFTP and folder items, which Stremio can't fetch itself.
func handleFile(w http.ResponseWriter, r *http.Request) {
	it := current.Load().Items[r.PathValue("key")]
	switch {
	case it == nil || it.InfoHash != "" || !servable(it.FileName): // only indexed media, checked again at serve time
		http.NotFound(w, r)
	case it.SMBHost != "":
		serveSMB(w, r, it)
	case it.DAVRoot != "":
		proxyTo(w, r, it, hostLogin("webdav", it.DAVRoot))
	case it.SFTPAddr != "":
		serveSFTP(w, r, it)
	case it.LocalFile != "":
		serveLocal(w, r, it)
	default:
		http.NotFound(w, r)
	}
}

// sameServer: u is on the same server as root.
func sameServer(u *url.URL, root string) bool {
	r, err := url.Parse(root)
	return err == nil && sameHost(u, r)
}

// proxyTo streams it.URL; Range/If-Range go upstream and 206/Content-Range come back untouched.
func proxyTo(w http.ResponseWriter, r *http.Request, it *Item, l Login) {
	target, err := url.Parse(it.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = ""
			pr.Out.Header.Del("Authorization")
			if l.User != "" && sameServer(target, it.DAVRoot) { // never hand the login to another host
				pr.Out.SetBasicAuth(l.User, l.Pass)
			}
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			debugf("proxy %s %s range=%q → %d", r.Method, it.Key, r.Header.Get("Range"), resp.StatusCode)
			return nil
		},
	}).ServeHTTP(w, r)
}
