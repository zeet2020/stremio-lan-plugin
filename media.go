package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// File kinds the scanners index besides plain videos: audio (grouped into
// albums by folder), .torrent files (each video inside becomes an item, played
// through Stremio's torrent engine) and playlists (.m3u/.m3u8/.pls).

var audioExts = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
	".opus": true, ".wav": true, ".wma": true,
}

var playlistExts = map[string]bool{".m3u": true, ".m3u8": true, ".pls": true}

const (
	maxTorrentSize  = 10 << 20 // .torrent files are small; anything bigger isn't one
	maxPlaylistSize = 2 << 20
)

// classify says what a file is: "video", "audio", "torrent", "playlist" or "".
// ".ts" is also TypeScript: real transport streams are big, code isn't.
func classify(name string, size int64) string {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case videoExts[ext]:
		if strings.Contains(strings.ToLower(name), "sample") || ext == ".ts" && size < 1<<20 {
			return ""
		}
		return "video"
	case audioExts[ext]:
		return "audio"
	case ext == ".torrent" && size <= maxTorrentSize:
		return "torrent"
	case playlistExts[ext] && size <= maxPlaylistSize:
		return "playlist"
	}
	return ""
}

// mediaFile reports whether a scanned file is worth indexing.
func mediaFile(name string, size int64) bool { return classify(name, size) != "" }

// servable: what /file may stream (torrents and playlists never are).
func servable(name string) bool {
	k := classify(name, 1<<30)
	return k == "video" || k == "audio"
}

// addFile indexes a scanned file by kind. read fetches the file's bytes and is
// only called for torrents and playlists (small files, read during the scan).
func (idx *Index) addFile(it *Item, read func() ([]byte, error)) {
	k := classify(it.FileName, it.Size)
	if !idx.kinds[k] { // not a kind this folder indexes (e.g. torrents, unless chosen)
		return
	}
	switch k {
	case "video":
		idx.add(it)
	case "audio":
		it.Kind = "audio"
		idx.add(it)
	case "torrent":
		b, err := read()
		if err != nil {
			debugf("torrent %s: %v", it.FileName, err)
			return
		}
		t, err := parseTorrent(b)
		if err != nil {
			debugf("torrent %s: %v", it.FileName, err)
			return
		}
		for _, f := range t.Files {
			if classify(f.Name, f.Size) != "video" {
				continue
			}
			c := *it
			c.Key = key(it.Key + ":" + strconv.Itoa(f.Index))
			c.Title = strings.TrimSuffix(f.Name, path.Ext(f.Name))
			c.FileName = f.Name
			c.Size = f.Size
			c.InfoHash, c.FileIdx, c.Trackers = t.InfoHash, f.Index, t.Trackers
			idx.add(&c)
		}
	case "playlist":
		b, err := read()
		if err != nil {
			debugf("playlist %s: %v", it.FileName, err)
			return
		}
		if it.Entries = parsePlaylist(b); len(it.Entries) == 0 {
			return
		}
		it.Kind = "playlist"
		idx.add(it)
	}
}

// --- .torrent (bencode) ---

type torrentFile struct {
	Index int
	Name  string // path inside the torrent, "/"-joined
	Size  int64
}

type torrentMeta struct {
	InfoHash string // hex SHA-1 of the bencoded info dict
	Name     string
	Files    []torrentFile
	Trackers []string
}

// parseTorrent reads the fields Stremio needs. The info hash is the SHA-1 of
// the info dict exactly as encoded in the file.
func parseTorrent(b []byte) (*torrentMeta, error) {
	d := &bdecoder{b: b}
	root, err := d.value()
	if err != nil {
		return nil, err
	}
	top, ok := root.(map[string]any)
	if !ok || d.infoEnd == 0 {
		return nil, errors.New("not a torrent")
	}
	info, ok := top["info"].(map[string]any)
	if !ok {
		return nil, errors.New("not a torrent: no info dict")
	}
	sum := sha1.Sum(b[d.infoStart:d.infoEnd])
	t := &torrentMeta{InfoHash: hex.EncodeToString(sum[:])}
	t.Name, _ = info["name"].(string)
	if files, ok := info["files"].([]any); ok { // multi-file
		for i, f := range files {
			fm, _ := f.(map[string]any)
			var parts []string
			for _, p := range asList(fm["path"]) {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			size, _ := fm["length"].(int64)
			t.Files = append(t.Files, torrentFile{Index: i, Name: strings.Join(parts, "/"), Size: size})
		}
	} else {
		size, _ := info["length"].(int64)
		t.Files = []torrentFile{{Index: 0, Name: t.Name, Size: size}}
	}
	seen := map[string]bool{}
	addTracker := func(v any) {
		if s, ok := v.(string); ok && s != "" && !seen[s] {
			seen[s] = true
			t.Trackers = append(t.Trackers, s)
		}
	}
	addTracker(top["announce"])
	for _, tier := range asList(top["announce-list"]) {
		for _, u := range asList(tier) {
			addTracker(u)
		}
	}
	return t, nil
}

func asList(v any) []any { l, _ := v.([]any); return l }

// bdecoder is a minimal bencode decoder that also records where the
// top-level "info" dict starts and ends.
type bdecoder struct {
	b                  []byte
	i                  int
	depth              int
	infoStart, infoEnd int
}

func (d *bdecoder) value() (any, error) {
	if d.i >= len(d.b) {
		return nil, errors.New("truncated torrent")
	}
	if d.depth++; d.depth > 64 {
		return nil, errors.New("torrent nested too deep")
	}
	defer func() { d.depth-- }()
	switch c := d.b[d.i]; {
	case c == 'i':
		end := bytes.IndexByte(d.b[d.i:], 'e')
		if end < 0 {
			return nil, errors.New("bad integer")
		}
		n, err := strconv.ParseInt(string(d.b[d.i+1:d.i+end]), 10, 64)
		d.i += end + 1
		return n, err
	case c == 'l':
		d.i++
		var l []any
		for d.i < len(d.b) && d.b[d.i] != 'e' {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		d.i++
		return l, nil
	case c == 'd':
		d.i++
		m := map[string]any{}
		for d.i < len(d.b) && d.b[d.i] != 'e' {
			k, err := d.str()
			if err != nil {
				return nil, err
			}
			start := d.i
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			if k == "info" && d.depth == 1 {
				d.infoStart, d.infoEnd = start, d.i
			}
			m[k] = v
		}
		d.i++
		return m, nil
	case c >= '0' && c <= '9':
		return d.str()
	}
	return nil, fmt.Errorf("bad torrent byte %q", d.b[d.i])
}

func (d *bdecoder) str() (string, error) {
	colon := bytes.IndexByte(d.b[d.i:], ':')
	if colon < 0 {
		return "", errors.New("bad string")
	}
	n, err := strconv.Atoi(string(d.b[d.i : d.i+colon]))
	start := d.i + colon + 1
	if err != nil || n < 0 || start+n > len(d.b) {
		return "", errors.New("bad string length")
	}
	d.i = start + n
	return string(d.b[start:d.i]), nil
}

// --- playlists ---

// PlaylistEntry is one line of a playlist: a URL, or a path relative to the
// playlist's folder (resolved against the index when played).
type PlaylistEntry struct {
	Title string `json:"title"`
	Ref   string `json:"ref"`
}

// parsePlaylist reads M3U/M3U8 (#EXTINF titles) and PLS (FileN/TitleN).
func parsePlaylist(b []byte) []PlaylistEntry {
	text := strings.TrimPrefix(string(b), "\ufeff")
	if strings.HasPrefix(strings.TrimSpace(text), "[playlist]") { // PLS
		files, titles := map[int]string{}, map[int]string{}
		for _, line := range strings.Split(text, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			lk := strings.ToLower(k)
			if n, err := strconv.Atoi(strings.TrimPrefix(lk, "file")); err == nil && strings.HasPrefix(lk, "file") {
				files[n] = v
			} else if n, err := strconv.Atoi(strings.TrimPrefix(lk, "title")); err == nil && strings.HasPrefix(lk, "title") {
				titles[n] = v
			}
		}
		var out []PlaylistEntry
		for n := 1; n <= len(files)+100 && len(out) < len(files); n++ {
			if f, ok := files[n]; ok {
				out = append(out, PlaylistEntry{Title: titles[n], Ref: f})
			}
		}
		return out
	}
	var out []PlaylistEntry
	title := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), maxPlaylistSize)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			if _, t, ok := strings.Cut(line, ","); ok { // #EXTINF:-1 tvg-logo="…",Title
				title = strings.TrimSpace(t)
			}
		case strings.HasPrefix(line, "#"):
		default:
			out = append(out, PlaylistEntry{Title: title, Ref: line})
			title = ""
		}
	}
	return out
}

// readLimited reads a small file (torrent/playlist) opened by any source.
func readLimited[F io.ReadCloser](f F, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxTorrentSize+1))
}

// davGet downloads a small WebDAV file with the host's login.
func davGet(u *url.URL, l Login) ([]byte, error) {
	req, _ := http.NewRequest("GET", u.String(), nil)
	if l.User != "" {
		req.SetBasicAuth(l.User, l.Pass)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxTorrentSize+1))
}
