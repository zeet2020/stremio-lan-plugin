package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benc encodes the few shapes a test torrent needs.
func benc(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%d:%s", len(x), x)
	case int:
		return fmt.Sprintf("i%de", x)
	case []any:
		s := "l"
		for _, e := range x {
			s += benc(e)
		}
		return s + "e"
	case [][2]any: // ordered dict
		s := "d"
		for _, kv := range x {
			s += benc(kv[0]) + benc(kv[1])
		}
		return s + "e"
	}
	panic("benc")
}

func seasonPack() (torrent []byte, infoHash string) {
	info := benc([][2]any{
		{"files", []any{
			[][2]any{{"length", 900_000_000}, {"path", []any{"Breaking.Bad.S01E01.mkv"}}},
			[][2]any{{"length", 120}, {"path", []any{"Subs", "S01E01.srt"}}},
			[][2]any{{"length", 950_000_000}, {"path", []any{"Breaking.Bad.S01E02.mkv"}}},
		}},
		{"name", "Breaking.Bad.S01"},
		{"piece length", 262144},
		{"pieces", "xxxxxxxxxxxxxxxxxxxx"},
	})
	sum := sha1.Sum([]byte(info))
	t := "d" + benc("announce") + benc("udp://tracker.example:1337") +
		benc("announce-list") + benc([]any{[]any{"udp://tracker.example:1337"}, []any{"udp://other.example:80"}}) +
		benc("info") + info + "e"
	return []byte(t), hex.EncodeToString(sum[:])
}

func TestParseTorrent(t *testing.T) {
	b, hash := seasonPack()
	m, err := parseTorrent(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHash != hash || len(m.Files) != 3 || m.Files[2].Name != "Breaking.Bad.S01E02.mkv" || m.Files[2].Index != 2 || len(m.Trackers) != 2 {
		t.Fatalf("parsed %+v", m)
	}
	for _, bad := range []string{"", "d", "not a torrent", "d4:infoi1ee", "l" + strings.Repeat("l", 100)} {
		if _, err := parseTorrent([]byte(bad)); err == nil {
			t.Errorf("parseTorrent(%q) accepted", bad)
		}
	}
}

func TestParsePlaylist(t *testing.T) {
	m3u := "\ufeff#EXTM3U\n#EXTINF:-1 tvg-logo=\"x\",News 24\nhttp://iptv.example/news.m3u8\n\nsongs/01 Intro.mp3\n# comment\n"
	got := parsePlaylist([]byte(m3u))
	if len(got) != 2 || got[0] != (PlaylistEntry{"News 24", "http://iptv.example/news.m3u8"}) || got[1].Ref != "songs/01 Intro.mp3" || got[1].Title != "" {
		t.Errorf("m3u: %+v", got)
	}
	pls := "[playlist]\nFile1=http://radio.example/stream\nTitle1=Radio\nFile2=../a.mp3\nNumberOfEntries=2\n"
	if got := parsePlaylist([]byte(pls)); len(got) != 2 || got[0].Title != "Radio" || got[1].Ref != "../a.mp3" {
		t.Errorf("pls: %+v", got)
	}
}

func TestAudioTorrentPlaylist(t *testing.T) {
	dir := t.TempDir()
	put := func(rel string, b []byte) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
	}
	put("Music/Album One/01 Intro.mp3", []byte("ID3abcdefgh"))
	put("Music/Album One/02 Song.flac", []byte("fLaC"))
	tor, hash := seasonPack()
	put("Torrents/bb.torrent", tor)
	put("Music/Album One/mix.m3u", []byte("#EXTM3U\n#EXTINF:1,Intro\n01 Intro.mp3\nmissing.mp3\nhttp://radio.example/live\n"))

	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	current.Store(newIndex())
	h := routes()
	drainJobs()

	// by default a folder indexes video, audio and playlists, not torrents
	if code, body := post(t, h, "/api/hosts", `{"kind":"local","folders":[{"path":"`+dir+`"}]}`); code != 204 {
		t.Fatalf("save → %d %s", code, body)
	}
	publish(rescanSource(<-jobs))
	if idx := current.Load(); len(idx.Shows) != 0 || len(idx.Albums) != 1 || len(idx.Playlists) != 1 {
		t.Fatalf("defaults: shows %d (want 0: torrents off), albums %d, playlists %d", len(idx.Shows), len(idx.Albums), len(idx.Playlists))
	}
	// turn torrents on for this folder
	if code, body := post(t, h, "/api/hosts", `{"id":"local:","folders":[{"path":"`+dir+`","kinds":["video","audio","playlist","torrent"]}]}`); code != 204 {
		t.Fatalf("save kinds → %d %s", code, body)
	}
	publish(rescanSource(<-jobs))
	idx := current.Load()
	if len(idx.Albums) != 1 || len(idx.Playlists) != 1 || len(idx.Shows["breakingbad"].Episodes) != 2 {
		t.Fatalf("albums %d, playlists %d, shows %+v", len(idx.Albums), len(idx.Playlists), idx.Shows)
	}

	tk := "/" + getSettings().AddonToken
	mf := decode(t, get(t, h, tk+"/manifest.json"))
	if !strings.Contains(fmt.Sprint(mf["catalogs"]), "lan-audio") || !strings.Contains(fmt.Sprint(mf["catalogs"]), "lan-playlists") {
		t.Errorf("manifest catalogs: %v", mf["catalogs"])
	}

	// audio: album → tracks → stream from the LAN
	albums := decode(t, get(t, h, tk+"/catalog/other/lan-audio.json"))["metas"].([]any)
	if len(albums) != 1 || albums[0].(map[string]any)["name"] != "Album One" {
		t.Fatalf("audio catalog: %v", albums)
	}
	alID := albums[0].(map[string]any)["id"].(string)
	tracks := decode(t, get(t, h, tk+"/meta/other/"+alID+".json"))["meta"].(map[string]any)["videos"].([]any)
	if len(tracks) != 2 || tracks[0].(map[string]any)["title"] != "01 Intro" {
		t.Fatalf("album tracks: %v", tracks)
	}
	urls := streamURLs(t, h, tk+"/stream/other/"+tracks[0].(map[string]any)["id"].(string)+".json")
	if len(urls) != 1 || !strings.Contains(urls[0], "/file/") {
		t.Fatalf("track stream: %v", urls)
	}
	if rec := get(t, h, strings.TrimPrefix(urls[0], "http://example.com")); rec.Code != 200 || rec.Body.String() != "ID3abcdefgh" {
		t.Errorf("track file: %d", rec.Code)
	}

	// torrent: each episode in the pack → infoHash + fileIdx + trackers
	st := decode(t, get(t, h, tk+"/stream/series/lan:s:breakingbad:1:2.json"))["streams"].([]any)
	if len(st) != 1 {
		t.Fatalf("torrent streams: %v", st)
	}
	s0 := st[0].(map[string]any)
	if s0["infoHash"] != hash || s0["fileIdx"] != float64(2) || s0["url"] != nil ||
		!strings.Contains(fmt.Sprint(s0["sources"]), "tracker:udp://other.example:80") {
		t.Errorf("torrent stream: %v", s0)
	}

	// playlist: a relative entry resolves to the indexed file, a missing one
	// plays nothing, a URL plays as is
	pls := decode(t, get(t, h, tk+"/catalog/other/lan-playlists.json"))["metas"].([]any)
	if len(pls) != 1 {
		t.Fatalf("playlist catalog: %v", pls)
	}
	plID := pls[0].(map[string]any)["id"].(string)
	entries := decode(t, get(t, h, tk+"/meta/other/"+plID+".json"))["meta"].(map[string]any)["videos"].([]any)
	if len(entries) != 3 {
		t.Fatalf("playlist entries: %v", entries)
	}
	want := []string{"/file/", "", "http://radio.example/live"}
	for i, w := range want {
		got := streamURLs(t, h, tk+"/stream/other/"+plID+":"+fmt.Sprint(i)+".json")
		if w == "" && len(got) != 0 || w != "" && (len(got) != 1 || !strings.Contains(got[0], w)) {
			t.Errorf("entry %d → %v, want %q", i, got, w)
		}
	}
	// a .torrent or playlist is never served as a file
	for _, it := range idx.All {
		if it.InfoHash != "" || it.Kind == "playlist" {
			if rec := get(t, h, tk+"/file/"+it.Key+"/x"); rec.Code != 404 {
				t.Errorf("%s served as a file: %d", it.FileName, rec.Code)
			}
		}
	}

	// drop audio from the folder: albums leave Stremio at once, no scan
	if code, _ := post(t, h, "/api/hosts", `{"id":"local:","folders":[{"path":"`+dir+`","kinds":["video","playlist","torrent"]}]}`); code != 204 {
		t.Fatal("save without audio")
	}
	if n := len(current.Load().Albums); n != 0 {
		t.Errorf("audio still listed after turning it off: %d albums", n)
	}
	if ks := getSettings().Hosts["local:"].Folders[0].Kinds; len(ks) != 3 {
		t.Errorf("saved kinds %v", ks)
	}
	// unknown kinds are dropped; none at all means the defaults
	if got := cleanKinds([]string{"torrent", "bogus", "video"}); len(got) != 2 || got[0] != "video" || got[1] != "torrent" {
		t.Errorf("cleanKinds: %v", got)
	}
	if cleanKinds(nil) != nil || cleanKinds([]string{"video", "audio", "playlist"}) != nil {
		t.Error("defaults should be stored as nil")
	}
}
