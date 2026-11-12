#!/usr/bin/env bash
# Wraps the desktop binary in an AppImage: build/AppDir → dist/*.AppImage.
# GTK 4 and WebKitGTK 6 come from the host system (not bundled): WebKit's
# helper processes don't relocate reliably inside an AppImage.
set -euo pipefail
version=${VERSION:?set VERSION}
appdir=build/AppDir
rm -rf "$appdir"
install -Dm755 stremio-lan-desktop "$appdir/usr/bin/stremio-lan-desktop"
install -Dm644 packaging/stremio-lan.desktop "$appdir/stremio-lan.desktop"
install -Dm644 packaging/appicon.png "$appdir/stremio-lan.png"
install -Dm644 packaging/appicon.png "$appdir/usr/share/icons/hicolor/512x512/apps/stremio-lan.png"
cat > "$appdir/AppRun" <<'RUN'
#!/bin/sh
exec "$(dirname "$(readlink -f "$0")")/usr/bin/stremio-lan-desktop" "$@"
RUN
chmod +x "$appdir/AppRun"
tool=build/appimagetool
[ -x "$tool" ] || curl -fsSL -o "$tool" https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage
chmod +x "$tool"
mkdir -p dist
ARCH=x86_64 "$tool" --appimage-extract-and-run "$appdir" "dist/Stremio_LAN_Library-$version-x86_64.AppImage"
