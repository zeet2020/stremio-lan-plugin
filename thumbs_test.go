package main

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestThumbnails(t *testing.T) {
	dir := t.TempDir()
	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, filepath.Join(dir, "settings.json"), nil
	defer func() { settingsFile = "" }()
	initSecrets()
	tk := "/" + getSettings().AddonToken
	h := routes()

	movie := filepath.Join(dir, "The.Matrix.1999.mkv")
	ep := filepath.Join(dir, "Breaking.Bad.S01E02.mkv")
	for _, f := range []string{movie, ep} {
		os.WriteFile(f, []byte("not a real video"), 0o644)
	}
	idx := newIndex()
	idx.add(&Item{Key: "a00000000001", Title: "The.Matrix.1999", FileName: filepath.Base(movie), LocalFile: movie})
	idx.add(&Item{Key: "b00000000002", Title: "Breaking.Bad.S01E02", FileName: filepath.Base(ep), LocalFile: ep})
	idx.finish()
	current.Store(idx)

	moviePoster := func() any {
		return decode(t, get(t, h, tk+"/catalog/movie/lan-movies.json"))["metas"].([]any)[0].(map[string]any)["poster"]
	}

	// off: a drawn poster, no thumbnail served
	os.MkdirAll(thumbDir(), 0o700)
	os.WriteFile(filepath.Join(thumbDir(), "a00000000001.jpg"), []byte("jpeg"), 0o600)
	os.WriteFile(filepath.Join(thumbDir(), "b00000000002.jpg"), []byte("jpeg"), 0o600)
	if p, _ := moviePoster().(string); !strings.Contains(p, tk+"/poster.png?") || !strings.Contains(p, "t=The+Matrix") {
		t.Errorf("thumbnails off, poster %q", p)
	}
	if rec := get(t, h, tk+"/thumb/a00000000001.jpg"); rec.Code != 404 {
		t.Errorf("thumbnails off, /thumb → %d", rec.Code)
	}

	// on: catalogs, episodes and /thumb use the generated files
	if code, body := post(t, h, "/api/library", `{"thumbnails":true}`); code != 204 {
		t.Fatalf("enable → %d %s", code, body)
	}
	for thumbsRunning.Load() { // nothing to make (files exist); let the run finish
		time.Sleep(10 * time.Millisecond)
	}
	if p, _ := moviePoster().(string); !strings.HasSuffix(p, tk+"/thumb/a00000000001.jpg") {
		t.Errorf("movie poster %q", p)
	}
	videos := decode(t, get(t, h, tk+"/meta/series/lan:s:breakingbad.json"))["meta"].(map[string]any)["videos"].([]any)
	if th, _ := videos[0].(map[string]any)["thumbnail"].(string); !strings.HasSuffix(th, "/thumb/b00000000002.jpg") {
		t.Errorf("episode thumbnail %q", th)
	}
	if rec := get(t, h, tk+"/thumb/a00000000001.jpg"); rec.Code != 200 || rec.Body.String() != "jpeg" {
		t.Errorf("/thumb → %d %q", rec.Code, rec.Body.String())
	}
	// not made yet: URL sent anyway (Stremio caches metas); /thumb draws a
	// stand-in, uncached, and puts the item first in line
	os.Remove(filepath.Join(thumbDir(), "a00000000001.jpg"))
	if p, _ := moviePoster().(string); !strings.HasSuffix(p, tk+"/thumb/a00000000001.jpg") {
		t.Errorf("pending thumbnail: poster %q", p)
	}
	thumbsRunning.Store(true) // hold the generator so the queue can be seen
	rec := get(t, h, tk+"/thumb/a00000000001.jpg")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Errorf("pending /thumb → %d %q %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"))
	}
	if k := <-thumbWanted; k != "a00000000001" {
		t.Errorf("queued %q, want the asked-for thumbnail", k)
	}
	thumbsRunning.Store(false)
	thumbFailed.Store("a00000000001", true) // couldn't be made: the drawn poster instead
	if p, _ := moviePoster().(string); !strings.Contains(p, "/poster.png?") {
		t.Errorf("failed thumbnail: poster %q", p)
	}
	thumbFailed.Clear()
	if rec := get(t, h, tk+"/thumb/..%2fsettings.json"); rec.Code != 404 {
		t.Errorf("/thumb outside index → %d", rec.Code)
	}

	// clear: files gone; refused while a run is going
	thumbsRunning.Store(true)
	if code, _ := post(t, h, "/api/library/clear-thumbs", `{}`); code != 409 {
		t.Errorf("clear while running → %d", code)
	}
	thumbsRunning.Store(false)
	post(t, h, "/api/library", `{"thumbnails":false}`) // so the clear doesn't start a run on the fake files
	if code, body := post(t, h, "/api/library/clear-thumbs", `{}`); code != 204 {
		t.Fatalf("clear → %d %s", code, body)
	}
	if n := thumbCount(); n != 0 {
		t.Errorf("%d thumbnails left after clear", n)
	}

	// own folder: refused if relative; files there that aren't ours survive a clear
	if code, _ := post(t, h, "/api/library", `{"thumbDir":"relative/thumbs"}`); code != 400 {
		t.Errorf("relative thumbDir → %d", code)
	}
	own := filepath.Join(dir, "my thumbs")
	if code, body := post(t, h, "/api/library", `{"thumbDir":"`+own+`"}`); code != 204 || thumbDir() != own {
		t.Fatalf("set thumbDir → %d %s, dir %q", code, body, thumbDir())
	}
	os.WriteFile(filepath.Join(own, "a00000000001.jpg"), []byte("jpeg"), 0o600)
	os.WriteFile(filepath.Join(own, "holiday.jpg"), []byte("mine"), 0o600)
	if n := thumbCount(); n != 1 {
		t.Errorf("thumbCount %d in own folder, want 1", n)
	}
	post(t, h, "/api/library/clear-thumbs", `{}`)
	if _, err := os.Stat(filepath.Join(own, "holiday.jpg")); err != nil {
		t.Errorf("clear deleted a file it didn't make: %v", err)
	}
	if n := thumbCount(); n != 0 {
		t.Errorf("%d thumbnails left in own folder after clear", n)
	}
	if code, _ := post(t, h, "/api/library", `{"thumbDir":""}`); code != 204 || thumbDir() != defaultThumbDir() {
		t.Errorf("reset thumbDir → %d, dir %q", code, thumbDir())
	}

	// real frame grab, when ffmpeg is installed: a 2 s clip (shorter than the
	// 2-minute seek, so the fallback has to kick in)
	ff := ffmpegPath()
	if ff == "" {
		t.Skip("no ffmpeg")
	}
	clip := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10", "-y", clip).CombinedOutput(); err != nil {
		t.Fatalf("make clip: %v %s", err, out)
	}
	jpg := filepath.Join(dir, "clip.jpg")
	if err := grabFrame(ff, clip, jpg); err != nil {
		t.Fatalf("grabFrame: %v", err)
	}
	if b, _ := os.ReadFile(jpg); len(b) < 3 || string(b[:3]) != "\xff\xd8\xff" {
		t.Errorf("not a JPEG: %d bytes", len(b))
	}
	if err := grabFrame(ff, movie, filepath.Join(dir, "bad.jpg")); err == nil {
		t.Error("grabFrame on a non-video succeeded")
	}
}

// ffmpegthumbnailer is preferred when installed; a file it fails on falls
// back to ffmpeg.
func TestThumbnailerFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	fakeThumbnailer(t)

	if got := thumbTool(); got != "ffmpegthumbnailer" {
		t.Fatalf("thumbTool %q, want ffmpegthumbnailer", got)
	}
	out := filepath.Join(dir, "a.jpg")
	if err := makeThumb(thumbTools(), "/videos/x.mkv", out); err != nil {
		t.Fatalf("makeThumb: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "fake" {
		t.Errorf("not made by ffmpegthumbnailer: %q", b)
	}

	t.Setenv("FAKE_FAIL", "1")
	if ffmpegPath() == "" {
		if err := makeThumb(thumbTools(), "/videos/x.mkv", out); err == nil || !strings.Contains(err.Error(), "cannot decode") {
			t.Errorf("failure not reported: %v", err)
		}
		return
	}
	clip := filepath.Join(dir, "clip.mp4")
	if msg, err := exec.Command(ffmpegPath(), "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10", "-y", clip).CombinedOutput(); err != nil {
		t.Fatalf("make clip: %v %s", err, msg)
	}
	out = filepath.Join(dir, "b.jpg")
	if err := makeThumb(thumbTools(), clip, out); err != nil {
		t.Fatalf("fallback to ffmpeg: %v", err)
	}
	if b, _ := os.ReadFile(out); len(b) < 3 || string(b[:3]) != "\xff\xd8\xff" {
		t.Errorf("fallback didn't make a JPEG: %d bytes", len(b))
	}
}

// fakeThumbnailer puts a stand-in ffmpegthumbnailer first on PATH: it writes
// "fake" (or fails with FAKE_FAIL set) and appends each input to $FAKE_LOG.
func fakeThumbnailer(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "ffmpegthumbnailer"), []byte(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) out=$2;; -i) in=$2;; esac; shift; done
[ -n "$FAKE_LOG" ] && echo "$in" >> "$FAKE_LOG"
[ -n "$FAKE_FAIL" ] && { echo "cannot decode" >&2; exit 1; }
printf fake > "$out"
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Thumbnails Stremio asks for are made before the rest of the library.
func TestThumbsAskedForFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	fakeThumbnailer(t)
	dir := t.TempDir()
	t.Setenv("FAKE_LOG", filepath.Join(dir, "order.log"))
	settings, settingsFile, db = Settings{Thumbnails: true}, filepath.Join(dir, "settings.json"), nil
	defer func() { settings, settingsFile = Settings{}, "" }()
	idx := newIndex()
	for _, k := range []string{"a00000000001", "a00000000002", "a00000000003"} {
		idx.add(&Item{Key: k, Title: "Film " + k, FileName: k + ".mkv", LocalFile: "/v/" + k + ".mkv"})
	}
	idx.finish()
	current.Store(idx)
	for len(thumbWanted) > 0 {
		<-thumbWanted
	}

	thumbWanted <- "a00000000003" // the tile on screen
	genThumbs()
	for thumbsRunning.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "order.log"))
	if got := strings.Fields(string(b)); len(got) != 3 || got[0] != "/v/a00000000003.mkv" {
		t.Errorf("made in order %v, want the asked-for one first", got)
	}
	if n := thumbCount(); n != 3 {
		t.Errorf("%d thumbnails made, want 3", n)
	}
}

func TestDrawPoster(t *testing.T) {
	long := strings.Repeat("Supercalifragilisticexpialidocious Wedding ", 10)
	for shape, size := range posterSizes {
		for _, title := range []string{"Wedding (encoded)", long, ""} {
			b, err := drawPoster(shape, title, "2019")
			if err != nil {
				t.Fatalf("%s %q: %v", shape, title, err)
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s %q: not a PNG: %v", shape, title, err)
			}
			if r := img.Bounds(); r.Dx() != size[0] || r.Dy() != size[1] {
				t.Errorf("%s: %v, want %v", shape, r.Size(), size)
			}
		}
	}
	if a, _ := drawPoster("poster", "Wedding", ""); !bytes.Equal(a, must(drawPoster("poster", "Wedding", ""))) {
		t.Error("same title drew a different poster") // URLs are cached for a week
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
