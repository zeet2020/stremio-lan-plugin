package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeDLNA serves a MediaServer with a nested device, relative controlURL,
// one item per Browse page (to exercise pagination) and duplicate items.
func fakeDLNA(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/rootDesc.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<friendlyName>FakeNAS</friendlyName><UDN>uuid:fake</UDN><deviceList><device>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType><controlURL>/ctl/ContentDir</controlURL></service></serviceList>
</device></deviceList></device></root>`)
	})
	movie := `<item id="m1"><dc:title>Inception.2010.1080p.BluRay.x264</dc:title><upnp:class>object.item.videoItem</upnp:class>
<res protocolInfo="http-get:*:video/mp4:DLNA.ORG_PN=transcode" size="100">%[1]s/media/small.mp4</res>
<res protocolInfo="http-get:*:video/x-matroska:*" size="1000" resolution="1280x720">%[1]s/media/1.mkv</res></item>`
	children := map[string][]string{
		"0": {`<container id="c1"><dc:title>Movies</dc:title></container>`, `<container id="c2"><dc:title>Shows</dc:title></container>`,
			`<container id="c1"><dc:title>Loop</dc:title></container>`},
		"c1": {movie, movie, `<item id="a1"><dc:title>song</dc:title><upnp:class>object.item.audioItem</upnp:class><res>%[1]s/a.mp3</res></item>`},
		"c2": {`<item id="e1"><dc:title>Breaking.Bad.S01E02.720p.WEB-DL</dc:title><upnp:class>object.item.videoItem.movie</upnp:class><res protocolInfo="http-get:*:video/x-matroska:*">%[1]s/media/2.mkv</res></item>`},
	}
	reID := regexp.MustCompile(`<ObjectID>(.*?)</ObjectID>`)
	reStart := regexp.MustCompile(`<StartingIndex>(\d+)</StartingIndex>`)
	mux.HandleFunc("/ctl/ContentDir", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("SOAPACTION"), "ContentDirectory:1#Browse") {
			t.Errorf("bad SOAPACTION %q", r.Header.Get("SOAPACTION"))
		}
		b, _ := io.ReadAll(r.Body)
		id := reID.FindStringSubmatch(string(b))[1]
		start, _ := strconv.Atoi(reStart.FindStringSubmatch(string(b))[1])
		kids := children[id]
		page, n := "", 0
		if start < len(kids) {
			page, n = fmt.Sprintf(kids[start], srv.URL), 1
		}
		d := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` + page + `</DIDL-Lite>`
		var esc strings.Builder
		xml.EscapeText(&esc, []byte(d))
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result><NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`,
			esc.String(), n, len(kids))
	})
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.mkv", time.Time{}, strings.NewReader("0123456789"))
	})
	mux.HandleFunc("/meta/movie/tt1375666.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"meta":{"name":"Inception","releaseInfo":"2010"}}`)
	})
	mux.HandleFunc("/meta/series/tt0903747.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"meta":{"name":"Breaking Bad","releaseInfo":"2008–2013"}}`)
	})
	dav := map[string]string{
		"/dav/": `<d:response><d:href>/dav/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/dav/Movies/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`,
		"/dav/Movies/": `<d:response><d:href>/dav/Movies/The%20Matrix%20(1999).mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>10</d:getcontentlength></d:prop></d:propstat></d:response>`,
	}
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "me" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == "PROPFIND" {
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">%s</d:multistatus>`, dav[r.URL.Path])
			return
		}
		http.ServeContent(w, r, "x.mkv", time.Time{}, strings.NewReader("0123456789"))
	})
	srv = httptest.NewServer(mux)
	return srv
}

func get(t *testing.T, h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: newSession()}) // admin; ignored by add-on routes
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad json %q: %v", rec.Body.String(), err)
	}
	return v
}

func streamURLs(t *testing.T, h http.Handler, path string) []string {
	var out []string
	for _, s := range decode(t, get(t, h, path))["streams"].([]any) {
		out = append(out, s.(map[string]any)["url"].(string))
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	fake := fakeDLNA(t)
	defer fake.Close()
	cinemetaBase = fake.URL
	cfg = Config{Servers: []string{fake.URL + "/rootDesc.xml"}, RootID: "0", MaxDepth: 12, MatchIMDB: true}
	settings, settingsFile = Settings{}, ""
	initSecrets()
	tk := "/" + getSettings().AddonToken

	idx := Build(context.Background())
	if len(idx.All) != 3 || len(idx.Movies) != 1 || len(idx.Shows) != 1 || len(idx.Albums) != 1 { // + one DLNA audio item
		t.Fatalf("index: %d items, %d movies, %d shows", len(idx.All), len(idx.Movies), len(idx.Shows))
	}
	current.Store(idx)
	h := routes()
	mv := idx.Movies[0]

	if rec := get(t, h, tk+"/manifest.json"); rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS")
	}
	metas := decode(t, get(t, h, tk+"/catalog/movie/lan-movies.json"))["metas"].([]any)
	if len(metas) != 1 || metas[0].(map[string]any)["name"] != "Inception" {
		t.Errorf("movie catalog: %v", metas)
	}
	if metas := decode(t, get(t, h, tk+"/catalog/series/lan-series/search=bad.json"))["metas"].([]any); len(metas) != 1 {
		t.Errorf("series search: %v", metas)
	}
	if metas := decode(t, get(t, h, tk+"/catalog/movie/lan-movies/skip=100.json"))["metas"].([]any); len(metas) != 0 {
		t.Errorf("skip: %v", metas)
	}
	videos := decode(t, get(t, h, tk+"/meta/series/lan:s:breakingbad.json"))["meta"].(map[string]any)["videos"].([]any)
	if len(videos) != 1 || videos[0].(map[string]any)["id"] != "lan:s:breakingbad:1:2" {
		t.Errorf("series meta: %v", videos)
	}

	wantMovie := fake.URL + "/media/1.mkv" // largest res, not the transcode
	for _, p := range []string{tk + "/stream/movie/lan:m:" + mv.Key + ".json", tk + "/stream/movie/tt1375666.json"} {
		if got := streamURLs(t, h, p); len(got) != 1 || got[0] != wantMovie {
			t.Errorf("%s → %v", p, got)
		}
	}
	for _, p := range []string{tk + "/stream/series/lan:s:breakingbad:1:2.json", tk + "/stream/series/tt0903747:1:2.json"} {
		if got := streamURLs(t, h, p); len(got) != 1 || got[0] != fake.URL+"/media/2.mkv" {
			t.Errorf("%s → %v", p, got)
		}
	}
	if got := streamURLs(t, h, tk+"/stream/movie/tt0000000.json"); len(got) != 0 {
		t.Errorf("unknown id → %v", got)
	}

	// explorer: browse the WebDAV server, pick a folder, scan just that host,
	// stream through /file with the login added
	post := func(path, body string, hdr ...string) (int, string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: newSession()})
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	conn := `"kind":"webdav","addr":"` + fake.URL + `/dav","user":"me","pass":"secret"`
	if code, _ := post("/api/browse", `{`+conn+`}`, "Origin", "http://evil.example"); code != 403 {
		t.Errorf("cross-origin POST → %d", code)
	}
	if code, _ := post("/api/browse", `{"kind":"webdav","addr":"ftp://x"}`); code != 400 {
		t.Errorf("bad addr → %d", code)
	}
	code, body := post("/api/browse", `{`+conn+`,"path":""}`)
	if code != 200 || !strings.Contains(body, `{"name":"Movies","path":"Movies"}`) {
		t.Fatalf("browse webdav → %d %s", code, body)
	}
	for len(jobs) > 0 {
		<-jobs
	}
	if code, body := post("/api/hosts", `{`+conn+`,"folders":[{"path":"Movies","label":"Movies"}]}`); code != 204 {
		t.Fatalf("save webdav host → %d %s", code, body)
	}
	davHost := "webdav:" + fake.URL + "/dav/"
	if id := <-jobs; id != davHost {
		t.Fatalf("queued %q, want %q", id, davHost)
	}
	publish(rescanSource(davHost))
	var mx []string
	for _, it := range current.Load().Movies {
		if it.DAVRoot != "" {
			mx = streamURLs(t, h, tk+"/stream/movie/lan:m:"+it.Key+".json")
		}
	}
	if len(mx) != 1 || !strings.HasPrefix(mx[0], "http://example.com"+tk+"/file/") || !strings.HasSuffix(mx[0], "/The%20Matrix%20%281999%29.mkv") {
		t.Fatalf("webdav stream: %v", mx)
	}
	rec := get(t, h, strings.TrimPrefix(mx[0], "http://example.com"), "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Errorf("webdav file: %d %q", rec.Code, rec.Body.String())
	}
	st := decode(t, get(t, h, "/api/status"))
	if n := len(st["hosts"].([]any)); n != 2 || strings.Contains(fmt.Sprint(st), "secret") {
		t.Errorf("status: %v", st)
	}

	// untick the folder: gone from Stremio at once, no scan
	if code, _ := post("/api/hosts", `{"id":"`+davHost+`","folders":[]}`); code != 204 {
		t.Fatalf("untick → %d", code)
	}
	<-jobs // the save queues a (no-op) scan of the host
	if n := len(current.Load().Movies); n != 1 {
		t.Errorf("unticked folder still listed: %d movies", n)
	}

	cfg.Proxy = true
	if got := streamURLs(t, h, tk+"/stream/movie/tt1375666.json"); len(got) != 1 || got[0] != "http://example.com"+tk+"/proxy/"+mv.Key {
		t.Errorf("proxy url: %v", got)
	}
	rec = get(t, h, tk+"/proxy/"+mv.Key, "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Errorf("proxy range: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
	}
}
