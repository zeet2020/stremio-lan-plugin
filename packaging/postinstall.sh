#!/bin/sh
# deb/rpm post-install: register the unit; on upgrade, restart it if it was
# running so the new version takes over. A fresh install isn't started.
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl try-restart stremio-lan.service >/dev/null 2>&1 || true
fi
if ! systemctl is-enabled --quiet stremio-lan.service 2>/dev/null; then
  echo "Stremio LAN Library installed. Start it with: sudo systemctl enable --now stremio-lan"
  echo "Then open http://<this machine>:7070/ to set the admin password."
fi
