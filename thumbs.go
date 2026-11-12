package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Thumbnails (opt-in, Settings → Generate thumbnails): after each scan,
// ffmpegthumbnailer (else ffmpeg) grabs one frame of every video that has no poster yet into the thumbnail
// folder (Settings, default thumbs/ next to settings.json). Catalogs show it as the poster, episode lists per episode.
// Files on SMB/WebDAV/SFTP are read through this server's own /file URL, so
// ffmpeg only fetches the bytes around the frame.

var (
	thumbsRunning atomic.Bool
	thumbWanted   = make(chan string, 256) // keys Stremio asked for before they were made: made first
	thumbFailed   sync.Map                 // item key → ffmpeg found no frame; not retried until restart
)

func thumbsOn() bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return settings.Thumbnails && settingsFile != ""
}

func defaultThumbDir() string { return filepath.Join(filepath.Dir(settingsFile), "thumbs") }

// thumbDir is where thumbnails are saved: the folder chosen in Settings, else
// thumbs/ next to settings.json.
func thumbDir() string {
	settingsMu.Lock()
	d := settings.ThumbDir
	settingsMu.Unlock()
	if d == "" {
		return defaultThumbDir()
	}
	return d
}

// validThumbDir checks a chosen thumbnail folder: a full path this server can
// create and write to. "" means the default.
func validThumbDir(p string) (string, error) {
	if p = strings.TrimSpace(p); p == "" {
		return "", nil
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", errors.New("need a full path, e.g. /media/usb/thumbnails")
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", fmt.Errorf("can't create %s: %w", p, err)
	}
	f, err := os.CreateTemp(p, ".write-test-*")
	if err != nil {
		return "", fmt.Errorf("can't write to %s: %w (the service can only write to /var/lib/stremio-lan unless you allow more with ReadWritePaths= in systemctl edit stremio-lan)", p, err)
	}
	f.Close()
	os.Remove(f.Name())
	return p, nil
}

// thumbFiles lists the thumbnails in the folder: only names this server
// makes (item key + .jpg), so a folder shared with other files is safe.
func thumbFiles() []string {
	if settingsFile == "" {
		return nil
	}
	ents, _ := os.ReadDir(thumbDir())
	var out []string
	for _, e := range ents {
		if k, ok := strings.CutSuffix(e.Name(), ".jpg"); ok && e.Type().IsRegular() && reKey.MatchString(k) {
			out = append(out, filepath.Join(thumbDir(), e.Name()))
		}
	}
	return out
}

var reKey = regexp.MustCompile(`^[0-9a-f]{12}$`) // key(): 12 hex digits

func thumbCount() int { return len(thumbFiles()) }

// clearThumbs deletes the thumbnails (and nothing else in their folder) and
// forgets past failures. With the setting on, they're made again from scratch.
func clearThumbs() error {
	if !thumbsRunning.CompareAndSwap(false, true) {
		return errThumbsBusy
	}
	var err error
	for _, f := range thumbFiles() {
		if e := os.Remove(f); e != nil && err == nil {
			err = e
		}
	}
	thumbFailed.Clear()
	thumbsRunning.Store(false)
	if err == nil {
		log.Printf("thumbnails: cleared %s", thumbDir())
		genThumbs()
	}
	return err
}

var errThumbsBusy = errors.New("thumbnails are being made; switch them off first, or try again when done")

func ffmpegPath() string {
	p, _ := exec.LookPath("ffmpeg")
	return p
}

// thumbTools are the installed programs that can make thumbnails, best
// first: ffmpegthumbnailer (made for this, one quick call), then ffmpeg.
func thumbTools() []string {
	var out []string
	for _, name := range []string{"ffmpegthumbnailer", "ffmpeg"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// thumbTool names the program thumbnails are made with, "" if none is installed.
func thumbTool() string {
	if t := thumbTools(); len(t) > 0 {
		return filepath.Base(t[0])
	}
	return ""
}

// wantsThumb: a video on the LAN without a server-provided poster.
func wantsThumb(it *Item) bool { return it.Kind == "" && it.InfoHash == "" && it.Poster == "" }

// thumbURL is the item's thumbnail URL, sent even before the file is made:
// Stremio caches metas and wouldn't ask again once it's ready. "" when the
// setting is off, the item doesn't get one, or ffmpeg couldn't make it.
func thumbURL(base string, it *Item) string {
	if it == nil || !thumbsOn() || !wantsThumb(it) {
		return ""
	}
	if _, failed := thumbFailed.Load(it.Key); failed {
		return ""
	}
	return base + "/thumb/" + it.Key + ".jpg"
}

// handleThumb serves a generated thumbnail. Only keys of indexed items are
// looked up, so the path can't point anywhere else.
func handleThumb(w http.ResponseWriter, r *http.Request) {
	k := strings.TrimSuffix(r.PathValue("file"), ".jpg")
	idx := current.Load()
	it := idx.Items[k]
	if !thumbsOn() || it == nil {
		http.NotFound(w, r)
		return
	}
	f := filepath.Join(thumbDir(), k+".jpg")
	if _, err := os.Stat(f); err != nil { // not made yet: a drawn stand-in, and Stremio asks again next time
		select { // someone's looking at it: make it next
		case thumbWanted <- k:
		default:
		}
		genThumbs()
		w.Header().Set("Cache-Control", "no-store")
		b, err := drawPoster("landscape", thumbTitle(idx, it), "Thumbnail coming")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(b)
		return
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	http.ServeFile(w, r, f)
}

// thumbTitle is what a pending thumbnail's stand-in shows: the movie's name,
// or the show and episode.
func thumbTitle(idx *Index, it *Item) string {
	if it.Parsed.Season > 0 {
		return fmt.Sprintf("%s S%02dE%02d", it.Parsed.Title, it.Parsed.Season, it.Parsed.Episode)
	}
	return idx.movieName(it)
}

// genThumbs makes the missing thumbnails of the current index in the
// background, one file at a time, those Stremio has asked for (thumbWanted)
// before the rest. If a run is already going it does nothing: files that run
// misses are picked up after the next scan.
// ponytail: a request landing as a run ends waits for the next run (scan or request).
func genThumbs() {
	if !thumbsOn() || !thumbsRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer thumbsRunning.Store(false)
		tools := thumbTools()
		if len(tools) == 0 {
			log.Printf("thumbnails: neither ffmpegthumbnailer nor ffmpeg found; install one to generate thumbnails")
			return
		}
		if err := os.MkdirAll(thumbDir(), 0o700); err != nil {
			log.Printf("thumbnails: %v", err)
			return
		}
		start, made, failed := time.Now(), 0, 0
		all, next := current.Load().All, 0
		for {
			if !thumbsOn() { // switched off meanwhile
				return
			}
			var it *Item
			select {
			case k := <-thumbWanted:
				it = current.Load().Items[k]
			default:
				if next < len(all) {
					it, next = all[next], next+1
				}
			}
			if it == nil {
				if next == len(all) && len(thumbWanted) == 0 {
					break // all done
				}
				continue // an asked-for item no longer in the library
			}
			out := filepath.Join(thumbDir(), it.Key+".jpg")
			if !wantsThumb(it) {
				continue
			}
			if _, err := os.Stat(out); err == nil {
				continue
			}
			if _, failed := thumbFailed.Load(it.Key); failed {
				continue
			}
			if err := makeThumb(tools, thumbInput(it), out); err != nil {
				debugf("thumbnail %s (%s): %v", it.Key, it.FileName, err)
				thumbFailed.Store(it.Key, true)
				failed++
				continue
			}
			made++
		}
		if made+failed > 0 { // nothing new: existing ones were all skipped
			log.Printf("thumbnails: %d made, %d failed (-v shows why) in %s", made, failed, time.Since(start).Round(time.Second))
		}
	}()
}

// thumbInput is what the thumbnailer reads: a local path, a DLNA URL, or this server's
// /file URL for SMB, WebDAV and SFTP (it adds the login).
func thumbInput(it *Item) string {
	switch {
	case it.LocalFile != "":
		return it.LocalFile
	case it.URL != "" && it.DAVRoot == "":
		return it.URL
	}
	return selfURL() + "/" + getSettings().AddonToken + "/file/" + it.Key + "/" + url.PathEscape(it.FileName)
}

// makeThumb tries each tool in turn: ffmpegthumbnailer can choke on files
// ffmpeg still reads.
func makeThumb(tools []string, in, out string) error {
	var errs []error
	for _, t := range tools {
		var err error
		if strings.HasPrefix(filepath.Base(t), "ffmpegthumbnailer") {
			err = grabThumbnailer(t, in, out)
		} else {
			err = grabFrame(t, in, out)
		}
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(t), err))
	}
	return errors.Join(errs...)
}

// grabThumbnailer writes a 640px-wide JPEG of the frame 10% into the video
// (past logos and intros).
func grabThumbnailer(ft, in, out string) error {
	tmp := out + ".tmp"
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	msg, err := exec.CommandContext(ctx, ft, "-i", in, "-o", tmp, "-s", "640", "-t", "10%", "-q", "8", "-c", "jpeg").CombinedOutput()
	if fi, serr := os.Stat(tmp); err == nil && serr == nil && fi.Size() > 0 {
		return os.Rename(tmp, out)
	}
	return fmt.Errorf("%v %s", err, strings.TrimSpace(string(msg)))
}

// grabFrame writes one 360p JPEG frame of in to out: 2 minutes in (past
// logos and intros), else near the start for short clips. The thumbnail
// filter keeps the most typical of the next 24 frames, so a fade or black
// frame doesn't become the picture.
func grabFrame(ff, in, out string) error {
	if !strings.Contains(in, "://") {
		in = "file:" + in // a local path; "file:" so a ':' in it isn't read as a protocol
	}
	tmp := out + ".tmp"
	defer os.Remove(tmp)
	var last error
	for _, at := range []string{"120", "3", "0"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		msg, err := exec.CommandContext(ctx, ff, "-nostdin", "-loglevel", "error", "-ss", at, "-i", in,
			"-frames:v", "1", "-vf", "thumbnail=24,scale=-2:360", "-q:v", "4", "-f", "image2", "-y", tmp).CombinedOutput()
		cancel()
		if fi, serr := os.Stat(tmp); err == nil && serr == nil && fi.Size() > 0 {
			return os.Rename(tmp, out)
		}
		last = fmt.Errorf("at %ss: %v %s", at, err, strings.TrimSpace(string(msg)))
	}
	return last
}
