package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFolderJobs(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	put := func(dir, name string) {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	put(a, "Inception.2010.mkv")
	put(b, "The.Matrix.1999.mkv")

	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	current.Store(newIndex())
	h := routes()
	drainJobs()
	movies := func() int { return len(current.Load().Movies) }

	// nothing is scanned until folders are chosen
	if publish(rescanSource("local:")); movies() != 0 {
		t.Fatalf("scanned without a chosen folder: %d", movies())
	}
	if code, _ := post(t, h, "/api/hosts", `{"kind":"local","folders":[{"path":"`+a+`"},{"path":"`+b+`"}]}`); code != 204 {
		t.Fatal("save")
	}
	publish(rescanSource(<-jobs))
	if movies() != 2 {
		t.Fatalf("after save: %d", movies())
	}

	// rescan one folder: picks up a new file there, the other folder untouched
	before := current.Load().Items
	put(b, "Blade.Runner.1982.mkv")
	put(a, "Alien.1979.mkv") // not picked up: folder a isn't rescanned
	if code, _ := post(t, h, "/api/sources/rescan", `{"id":"local:#`+b+`"}`); code != 202 {
		t.Fatal("rescan")
	}
	publish(rescanSource(<-jobs))
	if movies() != 3 {
		t.Fatalf("after folder rescan: %d", movies())
	}
	for k, it := range before {
		if it.SourceID == "local:#"+a && current.Load().Items[k] == nil {
			t.Errorf("folder rescan dropped %s", it.FileName)
		}
	}
	// rescan the host: both folders
	publish(rescanSource("local:"))
	if movies() != 4 {
		t.Fatalf("after host rescan: %d", movies())
	}

	// untick a folder: gone at once, before any scan runs
	drainJobs()
	if code, _ := post(t, h, "/api/hosts", `{"id":"local:","folders":[{"path":"`+b+`"}]}`); code != 204 {
		t.Fatal("untick")
	}
	if movies() != 2 {
		t.Fatalf("after untick: %d", movies())
	}
	// remove the host: everything gone at once
	if code, _ := post(t, h, "/api/hosts/remove", `{"id":"local:"}`); code != 204 || movies() != 0 {
		t.Fatalf("after remove: %d movies", movies())
	}
}

func TestMigrateSettings(t *testing.T) {
	// the old per-server model, shaped like a real settings.json
	s := Settings{
		SFTP:     map[string]Login{"sftp://192.168.0.105": {User: "me", HostKey: "k"}, "sftp://nas:2222/srv/media": {User: "u"}},
		SMB:      map[string]Login{"nas": {User: "smbuser"}},
		Allowed:  map[string]bool{"smb://nas/Movies": true},
		Disabled: map[string]bool{"smb://192.168.0.105/stream": true},
		Local:    []string{"/media/usb"},
		WebDAV:   map[string]Login{"https://cloud/dav/": {User: "w"}},
		DLNA:     []string{"http://dms/rootDesc.xml"},
	}
	if !migrateSettings(&s) {
		t.Fatal("not migrated")
	}
	want := map[string][]string{
		"sftp:sftp://192.168.0.105":    nil, // whole home dir: not carried over, pick folders again
		"sftp:sftp://nas:2222":         {"/srv/media"},
		"smb://nas":                    {"Movies"},
		"local:":                       {"/media/usb"},
		"webdav:https://cloud/dav/":    {""},
		"dlna:http://dms/rootDesc.xml": {"0"},
	}
	for id, paths := range want {
		h, ok := s.Hosts[id]
		if !ok {
			t.Errorf("missing host %s", id)
			continue
		}
		var got []string
		for _, f := range h.Folders {
			got = append(got, f.Path)
		}
		if len(got) != len(paths) || (len(paths) > 0 && got[0] != paths[0]) {
			t.Errorf("%s folders %v, want %v", id, got, paths)
		}
	}
	if s.Hosts["sftp:sftp://192.168.0.105"].HostKey != "k" || s.Hosts["smb://nas"].User != "smbuser" {
		t.Error("logins not kept")
	}
	if s.SFTP != nil || s.Allowed != nil || s.Disabled != nil || migrateSettings(&s) {
		t.Error("old fields not cleared, or migrated twice")
	}
}

// A finished index doesn't hold the one before it: otherwise every scheduled
// scan would keep the whole previous library in memory, forever.
func TestBuildDropsPrev(t *testing.T) {
	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, "", nil
	current.Store(newIndex())
	for range 2 {
		idx := build(context.Background(), true)
		if idx.prev != nil {
			t.Fatal("index keeps the previous one")
		}
		current.Store(idx)
	}
}

func TestEmptyIDRefused(t *testing.T) {
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	h := routes()
	drainJobs()
	for _, p := range []string{"/api/hosts/remove", "/api/sources/rescan"} {
		if code, _ := post(t, h, p, `{}`); code != 400 {
			t.Errorf("%s without id → %d, want 400", p, code)
		}
	}
	if len(jobs) != 0 {
		t.Error("empty id queued a scan")
	}
}
