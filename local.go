package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Folder sources: video folders on the machine running Stremio LAN Library (a USB
// disk on the Pi, an OS-mounted NAS share, ~/Videos on a desktop). Added in
// the UI by absolute path; served by the add-on at /file/{key}/{name}. Only
// indexed files are served, so nothing else on the disk is reachable.

// walkLocal indexes files under dir (a folder chosen on this machine). Linked
// folders are followed (a mounted drive is often reached through one), each
// real folder once; linked files are left out, as serveLocal refuses them.
func (idx *Index) walkLocal(dir, label string) error {
	visited := map[string]bool{}
	var walk func(d string, depth int) error
	walk = func(d string, depth int) error {
		if depth > cfg.MaxDepth {
			return nil
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			if visited[real] {
				return nil
			}
			visited[real] = true
		}
		ents, err := os.ReadDir(d)
		if err != nil {
			debugf("local %s: %v", d, err)
			if depth == 0 {
				return err
			}
			return nil // unreadable subfolder: skip it, keep scanning
		}
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(d, name)
			isDir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 {
				fi, err := os.Stat(p)
				isDir = err == nil && fi.IsDir()
			}
			if isDir {
				if !skipDir(filepath.Base(d), name) {
					walk(p, depth+1)
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil || !mediaFile(name, info.Size()) {
				continue
			}
			idx.addFile(&Item{
				Key: key("local:" + p), Server: label,
				Title: strings.TrimSuffix(name, filepath.Ext(name)), FileName: name,
				LocalFile: p, Size: info.Size(),
			}, func() ([]byte, error) { return readLimited(os.Open(p)) })
		}
		return nil
	}
	return walk(dir, 0)
}

// serveLocal streams an indexed local file with Range support.
func serveLocal(w http.ResponseWriter, r *http.Request, it *Item) {
	// Still a regular file? A symlink swapped in since the scan (say to
	// /etc/passwd) must not be followed.
	if fi, err := os.Lstat(it.LocalFile); err != nil || !fi.Mode().IsRegular() {
		http.Error(w, "file not available", http.StatusNotFound)
		return
	}
	f, err := os.Open(it.LocalFile)
	if err != nil {
		http.Error(w, "file not available", http.StatusNotFound)
		return
	}
	defer f.Close()
	var mod time.Time
	if fi, err := f.Stat(); err == nil {
		mod = fi.ModTime()
	}
	debugf("local serve %s range=%q", it.Key, r.Header.Get("Range"))
	http.ServeContent(w, r, it.FileName, mod, f)
}

// validFolder checks a folder source: an absolute path to a readable directory.
func validFolder(p string) (string, error) {
	p = filepath.Clean(strings.TrimSpace(p))
	if !filepath.IsAbs(p) {
		return "", errors.New("need a full path, e.g. /media/usb/Movies")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", errors.New("can't open " + p + ": " + err.Error())
	}
	if !fi.IsDir() {
		return "", errors.New(p + " is not a folder")
	}
	if _, err := os.ReadDir(p); err != nil {
		return "", errors.New("can't read " + p + ": " + err.Error())
	}
	return p, nil
}
