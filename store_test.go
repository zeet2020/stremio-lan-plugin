package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryPersistence(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "media")
	write := func(rel string, size int) {
		p := filepath.Join(media, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, size), 0o644)
	}
	write("Movies/Inception.2010.mkv", 10)
	write("Shows/Breaking.Bad.S01E02.mkv", 10)
	write("Recordings/News.2026.ts", 1<<20)         // real transport stream: kept
	write("code/app/index.d.ts", 900)               // TypeScript: skipped
	write("code/node_modules/x/Movie.2001.mkv", 10) // dependency folder: skipped
	write("go/pkg/mod/y/Clip.2002.mp4", 10)         // Go module cache: skipped

	cfg = Config{MaxDepth: 12, LocalDirs: []string{media}}
	settings, settingsFile = Settings{}, ""
	current.Store(newIndex())
	db = nil
	t.Cleanup(func() {
		if db != nil {
			db.Close()
			db = nil
		}
	})

	idx := Build(context.Background())
	if len(idx.All) != 3 || len(idx.Movies) != 2 || len(idx.Shows) != 1 {
		var names []string
		for _, it := range idx.All {
			names = append(names, it.FileName)
		}
		t.Fatalf("scan rules: got %v", names)
	}

	// save → reopen → same library, without scanning
	path := filepath.Join(dir, "library.db")
	if err := openDB(path); err != nil {
		t.Fatal(err)
	}
	if err := saveIndex(idx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = nil
	current.Store(newIndex())
	loadLibrary(path)
	got := current.Load()
	if len(got.All) != 3 || len(got.Movies) != 2 || len(got.Shows["breakingbad"].Episodes) != 1 ||
		len(got.Sources) != 1 || !got.Built.Equal(idx.Built) {
		t.Fatalf("reloaded: %d items, %d movies, sources %+v, built %v vs %v", len(got.All), len(got.Movies), got.Sources, got.Built, idx.Built)
	}
	if it := got.Items[idx.All[0].Key]; it == nil || it.LocalFile == "" || it.SourceID != "local:#"+media || it.Server != "This machine" {
		t.Fatalf("item fields lost: %+v", it)
	}

	// the folder goes away (USB unplugged): its files stay, marked stale
	os.RemoveAll(media)
	again := Build(context.Background())
	if src := again.Sources[0]; src.Error == "" || !src.Stale || len(again.All) != 3 {
		t.Fatalf("offline source: %+v, %d items", src, len(again.All))
	}
}
