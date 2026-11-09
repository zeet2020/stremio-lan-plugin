# Stremio LAN Library

A self-hosted [Stremio](https://www.stremio.com/) add-on that puts the videos, music and playlists already on your home network into Stremio.

It reads your **DLNA servers, SMB/Samba shares, WebDAV, SFTP servers and local folders**. In its web control panel you choose which folders to use, and they show up in Stremio:

- **LAN Movies** and **LAN Series** rows, plus your copy listed as a stream when you open the same title anywhere in Stremio.
- **LAN Audio** (albums by folder) and **LAN Playlists** (`m3u`, `pls`).

Files play straight from your network. Nothing is uploaded anywhere. Nothing is scanned until you pick the folders.

## Install the service

Download the package for your machine from [Releases](https://github.com/zeet2020/stremio-lan-plugin/releases):

| Machine | File |
|---|---|
| Raspberry Pi / Debian / Ubuntu | `stremio-lan_<version>_<arm64\|armhf\|amd64>.deb` |
| Fedora / RHEL / openSUSE | `stremio-lan-<version>.<arch>.rpm` |

```bash
sudo apt install ./stremio-lan_*_arm64.deb   # or: sudo dnf install ./stremio-lan-*.rpm
sudo systemctl enable --now stremio-lan       # start now and at every boot
```

Check it's running, and see its log:

```bash
systemctl status stremio-lan
journalctl -u stremio-lan -f
```

Stop or uninstall:

```bash
sudo systemctl stop stremio-lan
sudo apt remove stremio-lan    # settings stay in /var/lib/stremio-lan
```

## Set it up

1. Open `http://<machine-ip>:7070/` in a browser and **set an admin password**.
2. Click **Add source**, connect to a server (or this machine), tick the folders you want, then **Save and scan**.
3. Click **Open in Stremio** (or paste the shown link into Stremio → Add-ons). Log in to Stremio with the same account on your TV and phone, and the add-on is there too.

## Desktop app

Prefer a window and a tray icon? Get the **AppImage** (Linux) or the **Windows `.exe`** from [Releases](https://github.com/zeet2020/stremio-lan-plugin/releases). It's the same server with a built-in control panel, and it keeps running in the tray when you close the window.

## Settings

Most things are set in the control panel. A few environment variables (put them in `sudo systemctl edit stremio-lan`):

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `7070` | Web port |
| `RESCAN_MINUTES` | `30` | How often folders are rescanned |
| `SMB_DISCOVER` / `DLNA_DISCOVER` | `1` | `0` stops looking for servers on the network |
| `LOG_LEVEL` | `info` | `debug` logs every request |

**Forgot the admin password?** Stop the service, remove the `passwordHash` line from `/var/lib/stremio-lan/settings.json`, start it again and set a new one.

## Build from source

Needs Go ≥ 1.26 and Node 22.

```bash
make build          # service for this machine
make build-arm64    # service for a 64-bit Raspberry Pi
make desktop        # desktop app (Linux: libgtk-4-dev libwebkitgtk-6.0-dev)
make test
```

## Keep it private

It's meant for your home network. Don't expose port 7070 to the internet.

## License

[MIT](LICENSE)
