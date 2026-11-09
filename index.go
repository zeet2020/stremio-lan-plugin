package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"log"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Parsed struct {
	Title                 string
	Year, Season, Episode int
}

type Item struct {
	SourceID   string // Source.ID it was found in
	Kind       string // "" video, "audio", "playlist"; torrents are videos with InfoHash
	InfoHash   string // torrent items: played from the swarm, not from the LAN
	FileIdx    int
	Trackers   []string
	Entries    []PlaylistEntry // playlist items
	Album      string          // DLNA: the container it was found in (audio: its album; files elsewhere use their folder)
	Key        string          // sha1(serverUDN + resURL)[:12]
	Server     string          // friendlyName
	Title      string          // raw dc:title
	FileName   string          // last path segment of resURL, URL-decoded
	URL        string          // DLNA resource or WebDAV file URL; empty for SMB items
	DAVRoot    string          // WebDAV items: the base URL whose login applies
	SFTPAddr   string          // SFTP items: the source URL; Path is the remote path
	LocalFile  string          // folder items: absolute path on this machine
	SMBHost    string          // SMB items: host[:port], share and path inside the share,
	SMBShare   string          // served by the add-on at /file/{key}/{name}
	Path       string
	Size       int64
	Duration   string
	Resolution string
	Poster     string
	Parsed     Parsed
}

// DisplayName is the title plus the file's extension, e.g. "Inception.2010.1080p.mkv".
func (it *Item) DisplayName() string {
	ext := path.Ext(it.FileName)
	if strings.HasSuffix(strings.ToLower(it.Title), strings.ToLower(ext)) {
		return it.Title
	}
	return it.Title + ext
}

type Show struct {
	Slug, Name string
	Episodes   map[[2]int][]*Item
}

// Source is one chosen folder of a host: a row in the panel and a scan unit.
type Source struct {
	ID    string `json:"id"`   // host ID + "#" + folder path
	Host  string `json:"host"` // host ID
	Kind  string `json:"kind"` // local | dlna | smb | webdav | sftp
	Name  string `json:"name"` // folder label
	Addr  string `json:"addr"` // folder path
	Items int    `json:"items"`
	Error string `json:"error,omitempty"`
	Stale bool   `json:"stale,omitempty"` // scan failed; showing files from the last good scan
}

// Index is immutable once built; a rescan builds a new one and swaps the pointer.
type Index struct {
	Sources     []Source
	Albums      map[string]*Album // audio, grouped by folder
	Playlists   []*Item
	Discovered  []Discovered // servers found on the network (listed only, never scanned)
	Items       map[string]*Item
	All         []*Item
	Movies      []*Item            // sorted by title
	Shows       map[string]*Show   // by slug
	ByNorm      map[string][]*Item // normalized movie title → items
	Built       time.Time
	DAVStamps   map[string]string // WebDAV folder (base + path) → stamp at the last scan
	nameCount   map[string]int    // movie title+year → how many movies share it (set by finish)
	cur         string            // source being scanned; add() tags items with it
	prev        *Index            // last index, for reusing unchanged WebDAV folders
	incremental bool              // scheduled scan: skip WebDAV folders whose stamp didn't change
	kinds       map[string]bool   // content kinds the folder being scanned indexes
}

func newIndex() *Index {
	return &Index{Items: map[string]*Item{}, Shows: map[string]*Show{}, ByNorm: map[string][]*Item{}, Albums: map[string]*Album{}, DAVStamps: map[string]string{}}
}

// Album is the audio files of one folder.
type Album struct {
	Key, Name string
	Tracks    []*Item
}

// location is where an item lives within its source, so playlist entries
// (paths relative to the playlist) can be resolved to indexed items.
func (it *Item) location() string {
	switch {
	case it.LocalFile != "":
		return it.LocalFile
	case it.SMBShare != "":
		return it.SMBShare + "/" + strings.TrimPrefix(it.Path, "./")
	case it.SFTPAddr != "":
		return it.Path
	case it.URL != "":
		if u, err := url.Parse(it.URL); err == nil {
			return u.Path
		}
	}
	return ""
}

// folder is the item's folder, last two levels ("source1/encoded"), to tell
// same-named files apart. DLNA: the container it was found in.
func (it *Item) folder() string {
	if it.Album != "" {
		return it.Album
	}
	dir := path.Dir(strings.ReplaceAll(it.location(), "\\", "/"))
	if dir == "." || dir == "/" {
		return ""
	}
	if parent := path.Base(path.Dir(dir)); parent != "." && parent != "/" {
		return parent + "/" + path.Base(dir)
	}
	return path.Base(dir)
}

func nameKey(it *Item) string {
	return strings.ToLower(it.Parsed.Title) + "|" + strconv.Itoa(it.Parsed.Year)
}

// movieName is the title shown in Stremio. Movies sharing a title and year
// (an original and its encode, say) get their folder added: "Wedding (encoded)".
func (idx *Index) movieName(it *Item) string {
	n := it.Parsed.Title
	if n == "" {
		n = it.Title
	}
	if idx.nameCount[nameKey(it)] > 1 {
		if f := it.folder(); f != "" {
			n += " (" + path.Base(f) + ")"
		}
	}
	return n
}

// albumOf names an audio item's album: its folder.
func albumOf(it *Item) (key_, name string) {
	if it.Album != "" {
		return key(it.SourceID + "|" + it.Album), it.Album
	}
	dir := path.Dir(strings.ReplaceAll(it.location(), "\\", "/"))
	return key(it.SourceID + "|" + dir), path.Base(dir)
}

var current atomic.Pointer[Index]

func init() {
	current.Store(newIndex())
}

// Build lists the network's servers and scans every chosen folder in full.
func Build(ctx context.Context) *Index { return build(ctx, false) }

// build: incremental scans (the scheduled ones) reuse WebDAV folders whose
// ETag/last-modified didn't change; full ones (Rescan) open everything.
func build(ctx context.Context, incremental bool) *Index {
	start := time.Now()
	prev := current.Load()
	st := getSettings()
	idx := newIndex()
	idx.prev, idx.incremental = prev, incremental
	idx.Discovered = discoverHosts(ctx)
	seen := map[string]bool{}
	for _, h := range st.hostList() {
		idx.scanHost(h, nil, seen)
	}
	for i := range idx.Sources {
		idx.keepLastGood(prev, &idx.Sources[i])
	}
	idx.finish()
	idx.Built = time.Now()
	idx.prev = nil // only needed while scanning; kept, every index would hold all the old ones
	log.Printf("scan: %d folders, %d items (%d movies, %d shows), %d servers on the network, in %s",
		len(idx.Sources), len(idx.All), len(idx.Movies), len(idx.Shows), len(idx.Discovered), time.Since(start).Round(time.Millisecond))
	return idx
}

func key(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func dlnaItem(srv *Server, di *didlItem, r *didlRes) *Item {
	it := &Item{
		Key: key(srv.UDN + r.URL), Server: srv.Name, Title: strings.TrimSpace(di.Title),
		URL: r.URL, Size: r.Size, Duration: r.Duration, Resolution: r.Resolution, Poster: strings.TrimSpace(di.AlbumArt),
	}
	if u, err := url.Parse(r.URL); err == nil {
		it.FileName = path.Base(u.Path)
	}
	return it
}

// skipDirs are developer and system folders that never hold a library but can
// hold thousands of files (or a home directory full of them).
var skipDirs = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true, "site-packages": true,
	"__pycache__": true, "$RECYCLE.BIN": true, "System Volume Information": true, "lost+found": true,
}

// skipDir reports whether to skip folder name inside a folder named parent.
func skipDir(parent, name string) bool {
	return skipDirs[name] || name == "mod" && parent == "pkg" // Go module cache
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true, ".webm": true,
	".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true, ".flv": true,
}

// logSource reports one source's scan; errors always, the rest with LOG_LEVEL=debug.
func logSource(src Source, start time.Time) {
	took := time.Since(start).Round(time.Millisecond)
	switch {
	case src.Error != "":
		log.Printf("scan %s: ERROR %s (%s)", src.ID, src.Error, took)
	default:
		debugf("scan %s: %d files in %s", src.ID, src.Items, took)
	}
}

func (idx *Index) finish() {
	idx.nameCount = map[string]int{}
	for _, it := range idx.Movies {
		idx.nameCount[nameKey(it)]++
	}
	sort.Slice(idx.Movies, func(i, j int) bool {
		return strings.ToLower(idx.Movies[i].Parsed.Title) < strings.ToLower(idx.Movies[j].Parsed.Title)
	})
	for _, al := range idx.Albums {
		sort.Slice(al.Tracks, func(i, j int) bool {
			return strings.ToLower(al.Tracks[i].FileName) < strings.ToLower(al.Tracks[j].FileName)
		})
	}
	sort.Slice(idx.Playlists, func(i, j int) bool {
		return strings.ToLower(idx.Playlists[i].FileName) < strings.ToLower(idx.Playlists[j].FileName)
	})
}

// resolveEntry finds what a playlist entry points at: a URL as is, or a path
// relative to the playlist resolved to an indexed item of the same source.
func (idx *Index) resolveEntry(pl *Item, e PlaylistEntry) (url string, it *Item) {
	ref := strings.TrimSpace(e.Ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, nil
	}
	ref = strings.TrimPrefix(ref, "file://")
	ref = strings.ReplaceAll(ref, "\\", "/")
	want := ref
	if !strings.HasPrefix(ref, "/") {
		want = path.Join(path.Dir(strings.ReplaceAll(pl.location(), "\\", "/")), ref)
	}
	want = path.Clean(want)
	for _, c := range idx.All {
		if c.SourceID == pl.SourceID && c.Kind != "playlist" && path.Clean(strings.ReplaceAll(c.location(), "\\", "/")) == want {
			return "", c
		}
	}
	return "", nil
}

// keepLastGood: a folder that failed this scan (NAS asleep, network blip)
// keeps the files the previous scan found, instead of vanishing from Stremio.
func (idx *Index) keepLastGood(prev *Index, src *Source) {
	if prev == nil || src.Error == "" || src.Items > 0 {
		return
	}
	for _, it := range prev.All {
		if it.SourceID == src.ID && idx.Items[it.Key] == nil {
			c := *it
			idx.add(&c)
			src.Items++
		}
	}
	src.Stale = src.Items > 0
}

func (idx *Index) add(it *Item) {
	if it.SourceID == "" {
		it.SourceID = idx.cur
	}
	// MiniDLNA URLs are /MediaItems/42.mkv: the filename only helps if it carries a year or episode.
	p := parse(strings.TrimSuffix(it.FileName, path.Ext(it.FileName)))
	if p.Year == 0 && p.Season == 0 && it.Title != "" {
		p = parse(it.Title)
	}
	it.Parsed = p
	idx.Items[it.Key] = it
	idx.All = append(idx.All, it)

	switch it.Kind {
	case "audio":
		k, name := albumOf(it)
		al := idx.Albums[k]
		if al == nil {
			al = &Album{Key: k, Name: name}
			idx.Albums[k] = al
		}
		al.Tracks = append(al.Tracks, it)
		return
	case "playlist":
		idx.Playlists = append(idx.Playlists, it)
		return
	}

	if slug := normalize(p.Title); p.Season > 0 && slug != "" {
		sh := idx.Shows[slug]
		if sh == nil {
			sh = &Show{Slug: slug, Name: p.Title, Episodes: map[[2]int][]*Item{}}
			idx.Shows[slug] = sh
		}
		k := [2]int{p.Season, p.Episode}
		sh.Episodes[k] = append(sh.Episodes[k], it)
		return
	}
	idx.Movies = append(idx.Movies, it)
	n := normalize(p.Title)
	idx.ByNorm[n] = append(idx.ByNorm[n], it)
}

// --- filename parsing ---

var (
	reSep   = regexp.MustCompile(`[._\s]+`)
	reSxxEy = regexp.MustCompile(`(?i)^(.*?)[\s\-\[\(]*\bs(\d{1,2})[\s.\-]?e(\d{1,3})`)
	reNxNN  = regexp.MustCompile(`(?i)^(.*?)[\s\-\[\(]*\b(\d{1,2})x(\d{2,3})\b`)
	reYear  = regexp.MustCompile(`^(.*)[\s(\[]+((?:19|20)\d{2})(?:[\s)\]]|$)`) // greedy: last year wins
	reTags  = regexp.MustCompile(`^\s*(\[[^\]]*\]\s*)+`)                       // leading [site] / [group] tags
	reJunk  = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|hdr|bluray|bdrip|web[- ]?dl|webrip|hdtv|x264|x265|hevc|h\.?264|h\.?265|aac|dts|remux|proper|repack)\b`)
)

func cleanTitle(s string) string {
	if loc := reJunk.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	return strings.Trim(s, " -([")
}

func parse(name string) Parsed {
	s := strings.TrimSpace(reSep.ReplaceAllString(reTags.ReplaceAllString(name, ""), " "))
	for _, re := range []*regexp.Regexp{reSxxEy, reNxNN} {
		if m := re.FindStringSubmatch(s); m != nil {
			se, _ := strconv.Atoi(m[2])
			ep, _ := strconv.Atoi(m[3])
			return Parsed{Title: cleanTitle(m[1]), Season: se, Episode: ep}
		}
	}
	// Cut release junk first so "1080p" etc. can't confuse the year match.
	s = cleanTitle(s)
	if m := reYear.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[2])
		return Parsed{Title: cleanTitle(m[1]), Year: y}
	}
	return Parsed{Title: s}
}

// --- matching ---

var accents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "æ", "ae", "ç", "c",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i", "î", "i", "ï", "i",
	"ñ", "n", "ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "œ", "oe",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y", "ß", "ss",
)

func normalize(s string) string {
	s = accents.Replace(strings.ReplaceAll(strings.ToLower(s), "&", " and "))
	words := strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	return strings.Join(words, "")
}

func (idx *Index) matchMovie(name string, year int) []*Item {
	var out []*Item
	for _, it := range idx.ByNorm[normalize(name)] {
		if it.Parsed.Year == 0 || year == 0 || abs(it.Parsed.Year-year) <= 1 {
			out = append(out, it)
		}
	}
	return out
}

func (idx *Index) matchEpisode(name string, season, episode int) []*Item {
	if sh := idx.Shows[normalize(name)]; sh != nil {
		return sh.Episodes[[2]int{season, episode}]
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
