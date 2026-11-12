# Implementation plan: `stremio-lan` — LAN media (DLNA, SMB, WebDAV, SFTP) → Stremio add-on (Go, runs on a Raspberry Pi)

## 1. Goal

A small Go service on the Pi (`192.168.0.101`) that:

1. Finds DLNA/UPnP media servers on the LAN (MiniDLNA, Jellyfin, Plex, Serviio, router USB shares…).
2. Indexes their video files.
3. Exposes them to Stremio as an add-on:
   - **Own catalogs** — "LAN Movies" and "LAN Series" rows on the Stremio Board/Discover.
   - **IMDb matching** — when you open any normal title in Stremio (e.g. *Inception*, `tt1375666`), your local copy shows up as a stream option next to everything else.

Non-goals (v1): transcoding (Stremio's player / Rivulet handle that), writing to the DLNA server, music/photos.

Constraints: Go standard library only (no external modules), single static binary, runs alongside Rivulet Engine (`:11470`) without conflict.

---

## 2. Architecture

```
            ┌────────────── Raspberry Pi 192.168.0.101 ──────────────┐
 Stremio ──►│ stremio-lan :7070                                     │
  app       │  ├─ HTTP: /manifest.json /catalog /meta /stream        │
            │  ├─ Index (in-memory, rebuilt every N min)             │
            │  ├─ DLNA client: SSDP discover → describe → Browse     │──► DLNA server(s) on LAN
            │  └─ Cinemeta lookup (tt → name/year), cached           │──► v3-cinemeta.strem.io
            └────────────────────────────────────────────────────────┘
 Player ──────────── plays the DLNA file URL directly (or via /proxy) ──► DLNA server
```

Key point: the add-on only returns **URLs**. Video bytes flow DLNA server → player directly (unless proxy mode is on).

---

## 3. Project layout

```
stremio-lan/
├── go.mod                 module stremio-lan, go 1.22+
├── main.go                config, startup, HTTP server, scheduler
├── config.go              env var parsing
├── ssdp.go                SSDP M-SEARCH discovery
├── upnp.go                device description + ContentDirectory Browse (SOAP)
├── didl.go                DIDL-Lite XML types + parsing
├── index.go               Item/Show/Index structs, scan, rebuild, lookups
├── parse.go               filename → title/year/season/episode
├── parse_test.go          table tests for the parser
├── match.go               normalization + matching against Cinemeta names
├── cinemeta.go            Cinemeta client with cache
├── addon.go               Stremio protocol handlers (manifest/catalog/meta/stream)
├── proxy.go               optional Range-preserving stream proxy
├── tls.go                 optional self-signed HTTPS
├── Makefile               build / build-arm64 / build-armv7
└── deploy/stremio-lan.service
```

---

## 4. Configuration (environment variables)

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `7070` | HTTP port |
| `BIND_ADDRESS` | `0.0.0.0` | Set to `192.168.0.101` on the Pi |
| `HTTPS_PORT` | `0` (off) | Self-signed HTTPS port, e.g. `7443` |
| `DLNA_SERVERS` | *(empty)* | Comma-separated device-description URLs; skips SSDP if set (e.g. `http://192.168.0.50:8200/rootDesc.xml`) |
| `ROOT_OBJECT_ID` | `0` | Where to start browsing. MiniDLNA: `64` = "Browse folders" (avoids duplicate virtual views) |
| `RESCAN_MINUTES` | `30` | Re-index interval |
| `MAX_DEPTH` | `12` | Recursion limit for containers |
| `MATCH_IMDB` | `1` | Answer stream requests for `tt…` IDs |
| `PROXY_STREAMS` | `0` | Return `/proxy/...` URLs instead of raw DLNA URLs |
| `PUBLIC_URL` | *(derived from request Host)* | Base URL used in proxy links |
| `LOG_LEVEL` | `info` | `debug` logs every SOAP call |

---

## 5. Components

### 5.1 SSDP discovery (`ssdp.go`)

- Open UDP socket, send to `239.255.255.250:1900`:
  ```
  M-SEARCH * HTTP/1.1
  HOST: 239.255.255.250:1900
  MAN: "ssdp:discover"
  MX: 2
  ST: urn:schemas-upnp-org:device:MediaServer:1
  ```
- Send 2–3 times (UDP loss), read responses for 3 s.
- Parse the `LOCATION:` header from each response (case-insensitive); dedupe.
- Skip entirely if `DLNA_SERVERS` is set.
- Function: `Discover(ctx, timeout) ([]string, error)`.

### 5.2 Device description (`upnp.go`)

- `GET LOCATION` → XML. Extract:
  - `device/friendlyName` (shown in stream names, e.g. "MyNAS")
  - `device/UDN` (stable server ID)
  - The `service` whose `serviceType` starts with `urn:schemas-upnp-org:service:ContentDirectory:`, take `controlURL`.
- Resolve `controlURL` against `URLBase` if present, otherwise against `LOCATION` (`url.Parse(base).ResolveReference`).
- Also search nested `deviceList` (some servers nest the MediaServer device).

### 5.3 ContentDirectory Browse (`upnp.go`)

- `POST controlURL` with headers:
  - `Content-Type: text/xml; charset="utf-8"`
  - `SOAPACTION: "urn:schemas-upnp-org:service:ContentDirectory:1#Browse"` (use the exact service version from the description)
- Body:
  ```xml
  <?xml version="1.0" encoding="utf-8"?>
  <s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
    <s:Body>
      <u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">
        <ObjectID>{id}</ObjectID>
        <BrowseFlag>BrowseDirectChildren</BrowseFlag>
        <Filter>*</Filter>
        <StartingIndex>{start}</StartingIndex>
        <RequestedCount>200</RequestedCount>
        <SortCriteria></SortCriteria>
      </u:Browse>
    </s:Body>
  </s:Envelope>
  ```
- Response: `Result` (an **escaped** DIDL-Lite XML string — unmarshal the envelope first, then unmarshal `Result` again), `NumberReturned`, `TotalMatches`.
- Paginate until `start >= TotalMatches` or `NumberReturned == 0` (some servers report `TotalMatches=0`; stop on empty page).
- HTTP timeout 15 s per call; retry once.

### 5.4 DIDL-Lite parsing (`didl.go`)

Structs (namespaces: `dc` = `http://purl.org/dc/elements/1.1/`, `upnp` = `urn:schemas-upnp-org:metadata-1-0/upnp/`):

- `container`: `id` attr, `dc:title`
- `item`: `id` attr, `dc:title`, `upnp:class`, `upnp:albumArtURI`, `res` (repeated) with attrs `protocolInfo`, `size`, `duration`, `resolution`, text = URL

Keep items whose `upnp:class` starts with `object.item.videoItem`. If several `res`, prefer the one whose `protocolInfo` is `http-get:*:video/...` with the largest `size` (original file, not a transcode profile).

### 5.5 Scanner + index (`index.go`)

```go
type Item struct {
    Key       string // sha1(serverUDN + resURL)[:12]
    Server    string // friendlyName
    Title     string // raw dc:title
    FileName  string // last path segment of resURL, URL-decoded
    URL       string
    Size      int64
    Duration  string
    Resolution string
    Poster    string // albumArtURI
    Parsed    Parsed // from parse.go
}
type Show struct { Slug, Name string; Episodes map[[2]int][]*Item }
type Index struct {
    mu      sync.RWMutex
    Items   map[string]*Item
    Movies  []*Item            // Parsed.Season == 0
    Shows   map[string]*Show   // by slug
    ByNorm  map[string][]*Item // normalized title → items (for tt matching)
    Built   time.Time
}
```

- Scan: DFS from `ROOT_OBJECT_ID`, track visited container IDs, enforce `MAX_DEPTH`, dedupe items by `URL` (Plex/Jellyfin expose the same file under several virtual folders).
- Build into a **new** Index, then swap the pointer under the lock (requests never see a half-built index).
- Run at startup (in a goroutine — HTTP serves immediately, returns empty results until first scan finishes) and every `RESCAN_MINUTES`. Also `POST /rescan` triggers one.
- Log: servers found, containers visited, items indexed, duration.

### 5.6 Filename parsing (`parse.go`)

Input: `FileName` without extension; fall back to `Title` (MiniDLNA strips extensions from titles).

1. Replace `.` and `_` with spaces; collapse spaces.
2. **Episode** patterns (first match wins):
   - `(?i)^(.*?)[\s\-\[\(]*s(\d{1,2})[\s\.\-]?e(\d{1,3})`  → `Show.Name.S01E02`
   - `(?i)^(.*?)[\s\-\[\(]*(\d{1,2})x(\d{2,3})\b`        → `Show 1x02`
   - If neither but the parent folder looks like `Season 1` — optional, skip for v1.
3. **Movie**: `^(.*?)[\s\(\[]+((?:19|20)\d{2})(?:[\s\)\]]|$)` → title + year.
4. Otherwise: title = whole cleaned name, year = 0.
5. Strip trailing junk from title: everything from the first token matching `(?i)\b(2160p|1080p|720p|480p|4k|uhd|hdr|bluray|bdrip|web[- ]?dl|webrip|hdtv|x264|x265|hevc|h\.?264|h\.?265|aac|dts|remux|proper|repack)\b`.

```go
type Parsed struct { Title string; Year, Season, Episode int }
```

`parse_test.go` — table tests at minimum:

| Input | Expected |
|---|---|
| `Inception.2010.1080p.BluRay.x264` | Inception, 2010 |
| `The Matrix (1999)` | The Matrix, 1999 |
| `Breaking.Bad.S01E02.720p.WEB-DL` | Breaking Bad, S1E2 |
| `the office 2x05` | the office, S2E5 |
| `Blade Runner 2049 (2017)` | Blade Runner 2049, 2017 *(year = last match)* |
| `2001 A Space Odyssey 1968` | 2001 A Space Odyssey, 1968 |
| `Home Video` | Home Video, 0 |

(The last two rows: when several 4-digit years appear, use the **last** one as year.)

### 5.7 Matching (`match.go`)

- `normalize(s)`: lowercase → `&`→`and` → strip accents (simple map for common Latin chars) → remove non-alphanumerics → drop leading `the`.
- Movie match: `normalize(file.Title) == normalize(cinemeta.name)` **and** (`file.Year == 0` or `|file.Year - cinemetaYear| <= 1`).
- Series match: normalized show name equal; then exact season/episode.
- Index `ByNorm` at build time so lookups are O(1).

### 5.8 Cinemeta client (`cinemeta.go`)

- `GET https://v3-cinemeta.strem.io/meta/{movie|series}/{tt}.json` → `meta.name`, `meta.year` / `meta.releaseInfo` (e.g. `"2010"` or `"2008–2013"`; take the first 4 digits).
- In-memory cache `map[tt]result` with 24 h TTL; cache misses too (1 h) so unknown IDs don't hammer it.
- 5 s timeout. On failure, return no streams (never error out the response).

### 5.9 Stremio protocol (`addon.go`)

All responses: `Content-Type: application/json`, **`Access-Control-Allow-Origin: *`**, `Access-Control-Allow-Headers: *`; answer `OPTIONS` with 204. Add `Cache-Control: max-age=60` on catalog/meta.

**`GET /manifest.json`**
```json
{
  "id": "community.stremio-lan",
  "version": "0.1.0",
  "name": "LAN Library",
  "description": "Videos from DLNA, SMB, WebDAV and SFTP servers on your network",
  "resources": [
    "catalog",
    { "name": "meta",   "types": ["movie","series"], "idPrefixes": ["lan:"] },
    { "name": "stream", "types": ["movie","series"], "idPrefixes": ["lan:","tt"] }
  ],
  "types": ["movie", "series"],
  "catalogs": [
    { "type": "movie",  "id": "lan-movies", "name": "LAN Movies", "extra": [{"name":"search"},{"name":"skip"}] },
    { "type": "series", "id": "lan-series", "name": "LAN Series", "extra": [{"name":"search"},{"name":"skip"}] }
  ],
  "behaviorHints": { "configurable": false }
}
```
(Drop `"tt"` from stream `idPrefixes` when `MATCH_IMDB=0`.)

**Routes** — Stremio encodes extras as a path segment:
- `/catalog/{type}/{id}.json`
- `/catalog/{type}/{id}/{extra}.json` where `{extra}` = `search=foo` or `skip=100` or `search=foo&skip=100` (URL-decode, then `url.ParseQuery`)
- `/meta/{type}/{id}.json`
- `/stream/{type}/{id}.json` (IDs contain `:` — URL-decode the segment)

**IDs**
- Movie: `lan:m:{itemKey}`
- Series: `lan:s:{slug}`; episode video: `lan:s:{slug}:{season}:{episode}`
- Unparseable files with no year still go into "LAN Movies" (works for home videos).

**Catalog** → `{"metas":[...]}`, 100 per page, sorted by title; `search` = case-insensitive substring on title.
```json
{ "id": "lan:m:3f9a…", "type": "movie", "name": "Inception", "poster": "<albumArtURI or omitted>", "releaseInfo": "2010", "posterShape": "poster" }
```

**Meta** → `{"meta":{...}}`
- Movie: id, type, name, releaseInfo, poster, `description` = `"{server} · {resolution} · {size GB}"`.
- Series: id, type, name, `videos`: one per episode `{ "id": "lan:s:slug:1:2", "title": "S01E02", "season": 1, "episode": 2, "released": "1970-01-01T00:00:00.000Z" }` (Stremio sorts by season/episode; `released` avoids "upcoming" display bugs).

**Stream** → `{"streams":[...]}` — one per matching file (could be several copies/qualities):
```json
{
  "url": "http://192.168.0.50:8200/MediaItems/42.mkv",
  "name": "LAN\nMyNAS",
  "title": "Inception.2010.1080p.BluRay.x264.mkv\n💾 8.4 GB · 1920x800",
  "behaviorHints": { "notWebReady": true, "bingeGroup": "lan-{slug}", "filename": "…", "videoSize": 9019431321 }
}
```
- `lan:` IDs → direct lookup.
- `tt…` / `tt…:S:E` IDs → Cinemeta lookup → match (5.7).
- Unknown → `{"streams":[]}` (HTTP 200, never 404/500).
- `notWebReady: true` tells Stremio Web to route through its streaming server (your Rivulet), which can remux/transcode MKV/HEVC.

**Extra endpoints**
- `GET /` → small HTML page: status, servers found, item counts, last scan time, install link (`stremio://192.168.0.101:7070/manifest.json`).
- `GET /health` → `ok`
- `POST /rescan` → triggers scan, returns 202.

### 5.10 Proxy (`proxy.go`, only when `PROXY_STREAMS=1`)

- `GET|HEAD /proxy/{itemKey}` → look up item → `httputil.ReverseProxy` to `item.URL`, forwarding `Range`, `If-Range`, and returning `206`/`Content-Range`/`Accept-Ranges` unchanged. No buffering (`FlushInterval: -1`).
- Use when: the client can't reach the DLNA server directly, or you need everything behind one HTTPS origin (Stremio Web).
- Stream URL becomes `{PUBLIC_URL or scheme://Host}/proxy/{itemKey}`.

### 5.11 HTTPS (`tls.go`, only when `HTTPS_PORT>0`)

- On first start, generate ECDSA P-256 self-signed cert (SAN: `BIND_ADDRESS` IP, `127.0.0.1`, hostname), save to `~/.stremio-lan/cert.pem` / `key.pem`, reuse later.
- Serve the same mux on both ports.

---

## 6. Milestones (each ends with a check you can run)

**M1 — Discovery + Browse CLI** (`ssdp.go`, `upnp.go`, `didl.go`)
- Add a hidden flag: `./stremio-lan -scan` prints servers and every video item found, then exits.
- ✅ Lists your DLNA server's friendlyName and files with URLs; `curl -I <url>` on one returns `200` and `Accept-Ranges: bytes`.

**M2 — Parser** (`parse.go`, `parse_test.go`)
- ✅ `go test ./...` passes all table cases.

**M3 — Index + catalog/meta/stream for `lan:` IDs** (`index.go`, `addon.go`)
- ✅ `curl localhost:7070/manifest.json` valid JSON
- ✅ `curl localhost:7070/catalog/movie/dlna-movies.json` lists movies
- ✅ `curl localhost:7070/stream/movie/lan:m:<key>.json` returns a URL that plays in VLC/mpv.

**M4 — IMDb matching** (`match.go`, `cinemeta.go`)
- ✅ For a movie you have, e.g. Inception: `curl localhost:7070/stream/movie/tt1375666.json` returns your file.
- ✅ For a show episode: `curl 'localhost:7070/stream/series/tt0903747:1:2.json'` (Breaking Bad S1E2) returns your file.

**M5 — Install in Stremio**
- Desktop / Android: Add-ons → paste `http://192.168.0.101:7070/manifest.json` (or open `stremio://192.168.0.101:7070/manifest.json`).
- ✅ "LAN Movies" row appears on Board; opening a title you own shows a "DLNA MyNAS" stream; it plays.
- ⚠️ If a client refuses a plain-HTTP add-on URL, enable `HTTPS_PORT=7443` and install `https://192.168.0.101:7443/manifest.json` after opening that URL once in the same browser to accept the cert. Record which clients needed it.

**M6 — Proxy, HTTPS, polish** (`proxy.go`, `tls.go`, status page, rescan)
- ✅ With `PROXY_STREAMS=1`, seeking works (check for `206` in `LOG_LEVEL=debug`).
- ✅ Stremio Web (HTTPS) can play via proxy + HTTPS.

**M7 — Deploy as a service** (section 8)
- ✅ Survives reboot; `journalctl -u stremio-lan` shows a scan summary.

---

## 7. Local test setup (on the Pi, if you don't have a DLNA server handy)

```bash
sudo apt install -y minidlna ffmpeg
mkdir -p ~/Videos/Movies ~/Videos/Shows
# 10-second dummy files with realistic names
ffmpeg -f lavfi -i testsrc=duration=10:size=1280x720:rate=24 -c:v libx264 -pix_fmt yuv420p \
  ~/Videos/Movies/Inception.2010.1080p.BluRay.x264.mkv
ffmpeg -f lavfi -i testsrc=duration=10:size=1280x720:rate=24 -c:v libx264 -pix_fmt yuv420p \
  ~/Videos/Shows/Breaking.Bad.S01E02.720p.WEB-DL.mkv

sudo sed -i 's|^media_dir=.*|media_dir=V,/home/pi/Videos|' /etc/minidlna.conf
sudo usermod -aG pi minidlna        # or chmod o+rx on the folders
sudo systemctl restart minidlna
sudo minidlnad -R && sudo systemctl restart minidlna   # force rescan
# MiniDLNA description URL: http://192.168.0.101:8200/rootDesc.xml
```

Useful manual checks:
```bash
curl -s http://192.168.0.101:8200/rootDesc.xml | grep -A3 ContentDirectory
curl -s -X POST http://192.168.0.101:8200/ctl/ContentDir \
  -H 'Content-Type: text/xml; charset="utf-8"' \
  -H 'SOAPACTION: "urn:schemas-upnp-org:service:ContentDirectory:1#Browse"' \
  --data '<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><ObjectID>0</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>50</RequestedCount><SortCriteria></SortCriteria></u:Browse></s:Body></s:Envelope>'
```

---

## 8. Build & deploy

Build on the Pi (`sudo apt install golang` — needs Go ≥ 1.22; if the apt version is older, use the tarball from go.dev) or cross-compile on a PC:

```makefile
build:        ; CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o stremio-lan .
build-arm64:  ; CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/stremio-lan-arm64 .
build-armv7:  ; CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="-s -w" -o dist/stremio-lan-armv7 .
```
`uname -m` on the Pi: `aarch64` → arm64, `armv7l` → armv7.

Install to `/home/pi/Public/stremio-lan/stremio-lan`, then `deploy/stremio-lan.service`:

```ini
[Unit]
Description=DLNA to Stremio add-on
After=network-online.target
Wants=network-online.target

[Service]
User=pi
WorkingDirectory=/home/pi/Public/stremio-lan
ExecStart=/home/pi/Public/stremio-lan/stremio-lan
Environment=BIND_ADDRESS=192.168.0.101
Environment=PORT=7070
# Environment=DLNA_SERVERS=http://192.168.0.50:8200/rootDesc.xml
# Environment=ROOT_OBJECT_ID=64
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```
```bash
sudo cp deploy/stremio-lan.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now stremio-lan
journalctl -u stremio-lan -f
# firewall (if ufw):
sudo ufw allow from 192.168.0.0/24 to any port 7070 proto tcp
```

---

## 9. Known pitfalls

- **Duplicates** — Plex/Jellyfin/MiniDLNA expose the same file under many virtual folders ("All Videos", "By Date", "Folders"). Dedupe by `res` URL; for MiniDLNA set `ROOT_OBJECT_ID=64`.
- **SSDP blocked** — Docker bridge networks, some mesh Wi-Fi, and `ufw` drop multicast. Fallback: set `DLNA_SERVERS` explicitly.
- **Escaped DIDL** — `Result` is XML-inside-XML; unmarshal twice.
- **`TotalMatches=0`** — some servers report it wrong; stop paginating on an empty page instead.
- **Transcode `res` entries** — Plex/Serviio list multiple `res`; pick the original (largest size / matching file extension).
- **Expiring URLs** — some servers (Plex) embed session tokens; if streams 404 after a while, lower `RESCAN_MINUTES` or enable proxy mode and re-resolve on request.
- **Mixed content** — Stremio Web runs on HTTPS and can't load `http://` add-ons or streams; needs HTTPS + proxy (M6). Desktop/Android apps are the easy path.
- **Never port-forward 7070** — no auth, LAN only.

---

## 10. Possible v2

- Folder-based season detection (`Show/Season 1/01.mkv`).
- Use Cinemeta posters for `lan:` items once matched (prettier catalog).
- UPnP `Search` action where supported (faster than full browse on big libraries).
- SSDP `NOTIFY` listening to pick up servers that appear later.
- Merge into Rivulet Engine as a built-in add-on (upstream PR / feature request).

---

## 11. Added after v1: SMB, WebDAV, SFTP, web UI

- **Dependencies:** `github.com/hirochachacha/go-smb2`, `github.com/pkg/sftp` and `golang.org/x/crypto/ssh` (all pure Go, still a static CGO-free binary). These replace the "stdlib only" rule. Needs Go ≥ 1.25.
- **SMB** (`smb.go`): sweeps the /24 around each private interface address for TCP 445, logs in as guest (or with `SMB_USER`/`SMB_PASS`, or a per-host login from the UI), lists the shares (skipping `$` admin shares) and walks them. Files are served by the add-on at `/file/{key}/{name}` with Range support, opening one SMB session per request.
- **WebDAV** (`webdav.go`): only added by hand in the UI (there's no standard LAN discovery). It walks folders with `PROPFIND Depth: 1`. Playback goes through `/file/...`, where the add-on adds the Basic auth so the password never reaches Stremio.
- **SFTP** (`sftp.go`): added by hand in the UI as host[:port], folder (absolute; blank = the user's home) and user, stored as `sftp://host[:port]/folder`. Login is the password, or the Pi user's unencrypted `~/.ssh/id_{ed25519,ecdsa,rsa}` when the password is blank. Adding the source tests the login and records the server's host key (trust on first use); after that, a different key is refused. Files are served at `/file/...`, opening one SSH session per request.
- **Web UI** (`web/`, React + TanStack Query, built into the binary with `go:embed`): stats, install link, a list of sources with show/hide-in-Stremio switches, add/remove servers by hand, and rescan. API: `GET /api/status`, `POST /api/sources`, `POST /api/sources/toggle`, `POST /api/sources/remove`, `POST /rescan`. POSTs from other sites are refused (Origin check). There's no auth, so keep it on the LAN.
- **Settings** set in the UI are saved to `~/.stremio-lan/settings.json` (mode 0600, it holds passwords; override the path with `SETTINGS_FILE`).

| Var | Default | Purpose |
|---|---|---|
| `SMB_SERVERS` | *(empty)* | Comma-separated `host[:port]`; skips the subnet sweep if set |
| `SMB_DISCOVER` | `1` | `0` turns off the port-445 sweep |
| `SMB_USER` / `SMB_PASS` | *(guest)* | Default SMB login |
| `SETTINGS_FILE` | `~/.stremio-lan/settings.json` | Where UI settings are saved |

Build: `make build-arm64` (runs `npm ci && npm run build` in `web/` first). UI dev: run the binary on :7070, then `cd web && npm run dev`.

## 12. Auth

- **Web UI and API:** admin password. The first visit to the UI asks for one (whoever opens the page first sets it, so do that right after installing). It's stored as a bcrypt hash in `settings.json`. Login sets a 30-day HMAC-signed `HttpOnly`, `SameSite=Strict` cookie. A wrong password waits 1 s before answering. Changing the password rotates the signing key, which logs out every other browser. Every `/api/*` route except auth, plus `POST /rescan`, needs the cookie.
- **Stremio and players:** they can't log in, so every add-on route lives under a secret path, `/{token}/manifest.json`, `/{token}/catalog/…`, `/{token}/stream/…`, `/{token}/file/…`, `/{token}/proxy/…`. Without the right token you get 404. The UI shows the install link and can make a new token ("New link"), which breaks old installs.
- **Forgot the password:** stop the service, delete `passwordHash` from `~/.stremio-lan/settings.json`, start it and set a new one in the UI.
- **Not covered:** `/health` stays public.
- **Plain HTTP:** the password and token travel unencrypted on the LAN. Use `HTTPS_PORT` if that matters (the cookie gets `Secure` over HTTPS).

## 13. Two ways to run it: service or desktop app

One codebase, two builds picked by a Go build tag.

| | Service (default) | Desktop app (`-tags desktop`) |
|---|---|---|
| Entry point | `main.go` | `desktop.go` |
| Build | `make build` / `build-arm64` / `build-armv7` | `make desktop` |
| CGO | off: static binary, cross-compiles to the Pi | on: Wails v3 + GTK 4 / WebKitGTK 6 on Linux |
| UI | browser at `http://<host>:7070/` | native window (same page), plus a tray icon |
| Runs via | systemd (`deploy/stremio-lan.service`) | app launcher (`deploy/stremio-lan.desktop`) |

Both call `setup()` and `serve()` in `server.go`, so they serve Stremio and the LAN identically and share `~/.stremio-lan/settings.json`.

Desktop behaviour:
- The window loads the local server's URL, so login, cookies and the UI are the same as in a browser.
- Closing the window hides it and the server keeps serving Stremio. The tray menu has Open control panel, Rescan now and Quit. A second launch focuses the existing window (single instance).
- If the port is already taken (the service runs on the same machine), it doesn't start a second server; it just opens a window onto the running one.

Linux build deps: `sudo apt install build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev`. Install the launcher with `install -Dm755 stremio-lan-desktop ~/.local/bin/stremio-lan-desktop`, `install -Dm644 build/appicon.png ~/.local/share/icons/hicolor/512x512/apps/stremio-lan.png`, `install -Dm644 deploy/stremio-lan.desktop ~/.local/share/applications/stremio-lan.desktop`. To start at login, copy the `.desktop` file to `~/.config/autostart/`.

The Go toolchain is now ≥ 1.26 (Wails v3.1 requires it).

## 14. Folder sources

- Folders on the machine running LAN Library (USB disk, OS-mounted NAS share, `~/Videos`), added in the panel by absolute path or with `LOCAL_DIRS`. Validated on add: absolute, exists, is a readable directory.
- Scanned with `filepath.WalkDir`: video extensions only, hidden and `sample` files skipped, `MAX_DEPTH` respected, symlinks not followed.
- Served at `/{token}/file/{key}/{name}` with `http.ServeContent` (Range). Only indexed keys resolve. At serve time the file must still have a video extension and still be a regular file (`Lstat`), so a symlink swapped in after a scan isn't followed.
- Packaging: the systemd unit uses `ProtectHome=read-only`; Flatpak gets `xdg-videos`, `/media`, `/run/media`, `/mnt` read-only; Snap gets `home` and `removable-media` plugs.

## 15. Saved library (SQLite)

- `library.db` (pure-Go `modernc.org/sqlite`, WAL) next to `settings.json` holds the last scan: `sources`, `items`, `meta.built`. Written in one transaction after every scan; parsed titles are recomputed on load, so parser fixes apply to old rows.
- Startup loads it and serves immediately. The scheduler waits until `built + RESCAN_MINUTES` (settings changes and Rescan still scan at once); with no saved library the first scan runs right away.
- Every item carries its `SourceID`. If a source fails a scan and finds nothing, the previous scan's items for that source (or for `smb://host/…` under a failed host) are kept and the source is marked `stale`. Playback still re-checks access (e.g. SFTP host keys), so stale listings never bypass checks.
- Scan rules shared by SMB/WebDAV/SFTP/folders: `.ts` must be ≥ 1 MB; `node_modules`, `bower_components`, `vendor`, `site-packages`, `__pycache__`, `pkg/mod`, `$RECYCLE.BIN`, `System Volume Information`, `lost+found` are skipped.

## 16. Scan jobs, per-source rescans, discovered sources off by default

- One scheduler goroutine runs scan jobs from a queue (`jobs.go`): `""` = full scan, else a `Source.ID`. Queued jobs are coalesced; any full scan wins.
- Switching a source **off** needs no scan: `hideNow()` republishes the current index without that source's items. Switching **on**, adding, removing or the row's rescan button queues a scan of **that source only** (`rescanSource`), merged into the current index; the full-scan schedule doesn't move. An SMB share's rescan covers its host (`sourceGroup`).
- `publish()` (under a mutex) always strips switched-off sources, so a full scan that started before a switch-off can't bring the files back.
- Discovered sources (SSDP, SMB sweep) start hidden: `Settings.Allowed` lists the ones switched on; `Settings.Disabled` the ones switched off. Hand-added and env-configured sources start shown. `Source.Found` marks discovered ones; library.db schema v2 (an older db is dropped and rebuilt).
- `-v` / `LOG_LEVEL=debug`: every request logged (`http GET /<key>/… → 206 …`), key masked; each source's scan time; SOAP Browse failures include the UPnP error code.

## 17. Explorer: nothing scanned unless chosen

- Settings hold `hosts`: `{kind, addr, name, user/pass/hostKey, folders: [{path, label}]}`, keyed by host ID (`local:`, `smb://host`, `sftp:sftp://host[:port]`, `webdav:URL`, `dlna:URL`). Folder rows/scan units are `hostID#path`.
- Discovery (SSDP, SMB sweep) only lists servers (`Index.Discovered`); "Look again" runs a discover-only job.
- `POST /api/browse` lists one level (subfolders + video count) for the explorer; `POST /api/hosts` saves a host's folder selection (SFTP key pinned on save), drops unticked folders at once and queues a scan of that host; `POST /api/hosts/remove`; `POST /api/sources/rescan {id}` takes a host ID, a folder ID or `discover`.
- Old settings migrate once: folders, WebDAV roots, DLNA servers and SFTP paths carry over; whole SFTP home directories and discovered servers don't (pick folders again). library.db schema v3.

## 18. Audio, torrents, playlists

- `classify(name, size)` → video | audio | torrent (≤ 10 MB) | playlist (≤ 2 MB); scanners pass every file to `addFile` with a reader for its bytes (local/SMB/SFTP/WebDAV), used only for torrents and playlists. DLNA adds `object.item.audioItem`.
- Audio → `Index.Albums` keyed by folder (DLNA: container); catalog `other/lan-audio`, meta `lan:a:<album>` with tracks as videos, stream `lan:a:<album>:<item>`.
- `.torrent`: own bencode decoder (`media.go`), info hash = SHA-1 of the raw info dict; one item per video file (`InfoHash`, `FileIdx`, `Trackers`); streams are `{infoHash, fileIdx, sources: tracker:/dht:}`, never `/file`.
- Playlists (M3U/M3U8 `#EXTINF`, PLS): catalog `other/lan-playlists`, meta `lan:p:<item>`, entry streams resolve relative paths to an indexed file of the same source, else play `http(s)` URLs as is; unresolved entries return no stream.
- library.db schema v4 (kind, info_hash, file_idx, trackers, entries, album).

## 19. Content kinds per folder

- `Folder.Kinds` ⊆ {video, audio, playlist, torrent}; empty = defaults (video, audio, playlist). Torrents are opt-in per folder.
- Scans only index a folder's kinds (`Index.kinds`, checked in `addFile` and the DLNA walk). `withoutHidden` also drops items whose kind their folder no longer indexes, so turning a kind off is instant; turning one on rescans the host.
- UI: kind chips on each folder in the explorer's "Folders to scan" list and on library rows.

## 20. Incremental WebDAV scans (large / debrid libraries)

- PROPFIND asks for `getetag` and `getlastmodified`; each WebDAV folder's stamp (ETag, else last-modified) is stored in `Index.DAVStamps` and in library.db (`dav_dirs`, schema v5).
- The scheduler's timer scan is incremental (`build(ctx, true)`): a subfolder whose stamp matches the last scan is not opened; its previous files (and its subfolders' stamps) are reused. Folders without a stamp are always opened.
- Servers differ on whether deep changes bubble up to a folder's stamp, so every user-triggered scan (Rescan, a host or folder rescan, saving a host) is a full walk and refreshes the stamps.

## 21. Desktop app: tray and start at login

- `Settings.Desktop`: `quitOnClose` (default off = closing hides to the tray), `startHidden`, `autostart`. `GET/POST /api/desktop` exist only in the desktop build (`desktopRoutes`); the Settings drawer shows the section only in the app's window (`?desktop=1`).
- `-hidden` (used by autostart) or `startHidden` creates the window hidden; the tray icon opens it.
- Start at login: Linux XDG autostart entry (`$XDG_CONFIG_HOME/autostart/stremio-lan.desktop`; Flatpak/Snap/AppImage launch through their launcher), macOS LaunchAgent, Windows `HKCU\...\CurrentVersion\Run`. The OS entry is the source of truth for the switch.
- "Open in Stremio" in the app's window goes through `POST /api/open` (desktop only, admin, `stremio://` only), because the WebKit window can't follow the link itself.
