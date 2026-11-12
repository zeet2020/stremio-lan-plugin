//go:build desktop

package main

import (
	_ "embed"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed packaging/appicon.png
var appIcon []byte

// main runs the desktop app: the same server as the service, plus a native
// window onto the control panel and a tray icon. Closing the window keeps the
// server running for Stremio; Quit in the tray stops it.
func main() {
	if slices.Contains(os.Args[1:], "-v") {
		os.Setenv("LOG_LEVEL", "debug") // verbose, as in the service build
	}
	setup()
	desktopRoutes = desktopAPI
	panel, err := serve()
	owner := err == nil
	if err != nil {
		// Port taken: if it's us (the service, or another copy), just show it.
		// Probing /health works on every OS, unlike matching EADDRINUSE.
		if !alreadyRunning(panel) {
			log.Fatal(err)
		}
		log.Printf("port %d in use; opening the instance already running at %s", cfg.Port, panel)
	}

	var win *application.WebviewWindow
	show := func() {
		win.Show()
		win.Focus()
	}
	app := application.New(application.Options{
		Name:        "Stremio LAN Library",
		Description: "Serves videos from DLNA, SMB, WebDAV and SFTP servers on your network to Stremio",
		Icon:        appIcon,
		// The window loads the local server, so Wails serves no assets itself.
		Assets:  application.AssetOptions{Handler: http.NotFoundHandler()},
		Mac:     application.MacOptions{ActivationPolicy: application.ActivationPolicyRegular},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ProgramName: "stremio-lan"},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "community.stremio-lan",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { show() },
		},
	})
	openExternal = app.Browser.OpenURL
	win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "Stremio LAN Library",
		Width:            1240,
		Height:           880,
		MinWidth:         400,
		MinHeight:        500,
		URL:              panel + "?desktop=1", // the page then opens stremio:// links via /api/open
		BackgroundColour: application.NewRGB(12, 13, 15),
		// -hidden: launched at login; or the user chose to start in the tray
		Hidden: slices.Contains(os.Args[1:], "-hidden") || getSettings().Desktop.StartHidden,
	})
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if getSettings().Desktop.QuitOnClose {
			app.Quit()
			return
		}
		win.Hide() // keep serving Stremio from the tray
		e.Cancel()
	})

	menu := app.NewMenu()
	menu.Add("Open control panel").OnClick(func(*application.Context) { show() })
	if owner {
		menu.Add("Rescan now").OnClick(func(*application.Context) { queueRescan() })
	}
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { app.Quit() })
	tray := app.SystemTray.New()
	if runtime.GOOS == "linux" {
		tray.SetLabel("Stremio LAN Library") // the tray item's name; Wails defaults it to "Wails" (on macOS a label is text beside the icon)
	}
	tray.SetIcon(appIcon)
	tray.SetTooltip("Stremio LAN Library")
	tray.SetMenu(menu)
	tray.OnClick(show)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func alreadyRunning(panel string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(panel + "health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	return resp.StatusCode == 200 && string(b) == "ok"
}

// desktopAPI: the panel's "Desktop app" settings (shown only in the app's window).
func desktopAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/desktop", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		p := getSettings().Desktop
		p.Autostart = autostartEnabled() // the OS entry is the truth (it may be removed by hand)
		writeJSON(w, p, false)
	}))
	mux.HandleFunc("POST /api/desktop", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var p DesktopPrefs
		if !readJSON(w, r, &p) {
			return
		}
		if err := setAutostart(p.Autostart); err != nil {
			http.Error(w, "start at login: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("desktop: quitOnClose=%v startHidden=%v autostart=%v", p.QuitOnClose, p.StartHidden, p.Autostart)
		saved(w, saveSettings(func(s *Settings) { s.Desktop = p }))
	}))
}
