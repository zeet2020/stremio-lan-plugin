//go:build desktop && linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopPrefsAndAutostart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("SNAP_NAME", "")
	t.Setenv("APPIMAGE", "")
	cfg = Config{}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	desktopRoutes = desktopAPI
	t.Cleanup(func() { desktopRoutes = nil })
	h := routes()

	if code, body := post(t, h, "/api/desktop", `{"quitOnClose":true,"startHidden":true,"autostart":true}`); code != 204 {
		t.Fatalf("save → %d %s", code, body)
	}
	if d := getSettings().Desktop; !d.QuitOnClose || !d.StartHidden || !d.Autostart {
		t.Errorf("saved %+v", d)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "autostart", "stremio-lan.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if !strings.Contains(string(b), `Exec="`+exe+`" "-hidden"`) {
		t.Errorf("autostart entry:\n%s", b)
	}
	if got := decode(t, get(t, h, "/api/desktop")); got["autostart"] != true {
		t.Errorf("GET → %v", got)
	}

	// packaged copies start through their launcher
	t.Setenv("FLATPAK_ID", "community.stremiolan.StremioLan")
	if c := launchCommand(); strings.Join(c, " ") != "flatpak run community.stremiolan.StremioLan -hidden" {
		t.Errorf("flatpak launch: %v", c)
	}
	t.Setenv("FLATPAK_ID", "")

	if code, _ := post(t, h, "/api/desktop", `{"autostart":false}`); code != 204 || autostartEnabled() {
		t.Errorf("autostart off → %d, still enabled %v", code, autostartEnabled())
	}
}
