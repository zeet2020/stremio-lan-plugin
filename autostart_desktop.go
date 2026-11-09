//go:build desktop && !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Start at login, hidden in the tray. Linux: an XDG autostart entry; macOS: a
// LaunchAgent. (Windows: autostart_windows.go.)

func autostartPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", "community.stremio-lan.plist")
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "autostart", "stremio-lan.desktop")
}

// launchCommand is how to start this copy again: packaged apps are started
// through their launcher, not the binary path inside their sandbox/mount.
func launchCommand() []string {
	switch {
	case os.Getenv("FLATPAK_ID") != "":
		return []string{"flatpak", "run", os.Getenv("FLATPAK_ID"), "-hidden"}
	case os.Getenv("SNAP_NAME") != "":
		return []string{"/snap/bin/" + os.Getenv("SNAP_NAME"), "-hidden"}
	case os.Getenv("APPIMAGE") != "":
		return []string{os.Getenv("APPIMAGE"), "-hidden"}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
	}
	return []string{exe, "-hidden"}
}

func autostartEnabled() bool {
	_, err := os.Stat(autostartPath())
	return err == nil
}

func setAutostart(on bool) error {
	p := autostartPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	cmd := launchCommand()
	var body string
	if runtime.GOOS == "darwin" {
		var args strings.Builder
		for _, a := range cmd {
			fmt.Fprintf(&args, "<string>%s</string>", xmlEscape(a))
		}
		body = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>community.stremio-lan</string>
<key>ProgramArguments</key><array>` + args.String() + `</array>
<key>RunAtLoad</key><true/>
</dict></plist>
`
	} else {
		quoted := make([]string, len(cmd))
		for i, a := range cmd {
			quoted[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(a) + `"`
		}
		body = "[Desktop Entry]\nType=Application\nName=Stremio LAN Library\nComment=Serve your LAN's videos to Stremio\n" +
			"Exec=" + strings.Join(quoted, " ") + "\nIcon=stremio-lan\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}
