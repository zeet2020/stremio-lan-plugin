#!/bin/sh
# deb/rpm pre-remove: stop and disable the service on uninstall, not on
# upgrade (deb passes "remove", rpm passes 0 when the last version goes).
case "$1" in
  remove|purge|0)
    if command -v systemctl >/dev/null 2>&1; then
      systemctl disable --now stremio-lan.service >/dev/null 2>&1 || true
    fi
    ;;
esac
