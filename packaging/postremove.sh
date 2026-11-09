#!/bin/sh
# deb/rpm post-remove: forget the unit file. Settings and the library in
# /var/lib/stremio-lan are kept (delete that folder to remove them too).
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
