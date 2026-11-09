package main

import (
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
	"golang.org/x/crypto/ssh"
)

// Nothing is scanned unless the user picks it. A Host is a server (or this
// machine) the user connected to in the explorer; only its chosen Folders are
// scanned, each with its subfolders. Servers found on the network are only
// listed (Discovered) until the user opens one and picks folders.

// Folder is one folder chosen in the explorer. Path means, per host kind:
// local an absolute path, smb "share/dir", sftp an absolute path, webdav a
// path below the base URL, dlna a container ID. Label is what the UI shows.
type Folder struct {
	Path  string   `json:"path"`
	Label string   `json:"label,omitempty"`
	Kinds []string `json:"kinds,omitempty"` // content to index here; empty = defaultKinds
}

// Content kinds a folder can contribute. Torrents are opt-in per folder:
// they play from the internet swarm, not the LAN, which surprises people.
var (
	allKinds     = []string{"video", "audio", "playlist", "torrent"}
	defaultKinds = []string{"video", "audio", "playlist"}
)

// kindSet is the folder's effective content kinds.
func (f Folder) kindSet() map[string]bool {
	ks := f.Kinds
	if len(ks) == 0 {
		ks = defaultKinds
	}
	set := map[string]bool{}
	for _, k := range ks {
		set[k] = true
	}
	return set
}

// cleanKinds keeps known kinds in a fixed order; none or the defaults → nil.
func cleanKinds(ks []string) []string {
	var out []string
	for _, k := range allKinds {
		if slices.Contains(ks, k) {
			out = append(out, k)
		}
	}
	if len(out) == 0 || slices.Equal(out, defaultKinds) {
		return nil
	}
	return out
}

// kindList is the folder's effective kinds, for the UI.
func (f Folder) kindList() []string {
	if len(f.Kinds) == 0 {
		return defaultKinds
	}
	return f.Kinds
}

// itemKind is the content kind an indexed item counts as.
func itemKind(it *Item) string {
	switch {
	case it.InfoHash != "":
		return "torrent"
	case it.Kind == "":
		return "video"
	}
	return it.Kind
}

type Host struct {
	Kind    string   `json:"kind"` // local | dlna | smb | webdav | sftp
	Addr    string   `json:"addr"` // "" (local) | description URL | host[:port] | base URL | sftp://host[:port]
	Name    string   `json:"name,omitempty"`
	Login            // user/pass/hostKey; never sent to the UI
	Folders []Folder `json:"folders,omitempty"`
}

// Discovered is a server found on the network that isn't configured yet.
type Discovered struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Addr string `json:"addr"`
	Name string `json:"name"`
}

func hostID(kind, addr string) string {
	switch kind {
	case "local":
		return "local:"
	case "smb":
		return "smb://" + addr
	}
	return kind + ":" + addr // dlna:URL, webdav:URL, sftp:sftp://host[:port]
}

func folderID(host, p string) string { return host + "#" + p }

// inGroup: id is the folder/host group itself or a folder of that host.
func inGroup(id, group string) bool { return id == group || strings.HasPrefix(id, group+"#") }

func hostLogin(kind, addr string) Login { return getSettings().Hosts[hostID(kind, addr)].Login }

// hostList is every host to scan: the configured ones plus env config
// (LOCAL_DIRS adds folders on this machine, DLNA_SERVERS whole servers).
func (s Settings) hostList() []Host {
	hosts := maps.Clone(s.Hosts)
	if hosts == nil {
		hosts = map[string]Host{}
	}
	if len(cfg.LocalDirs) > 0 {
		h := hosts["local:"]
		h.Kind = "local"
		for _, d := range cfg.LocalDirs {
			if !slices.ContainsFunc(h.Folders, func(f Folder) bool { return f.Path == d }) {
				h.Folders = append(h.Folders, Folder{Path: d, Label: d})
			}
		}
		hosts["local:"] = h
	}
	for _, loc := range cfg.Servers {
		if id := hostID("dlna", loc); len(hosts[id].Folders) == 0 {
			hosts[id] = Host{Kind: "dlna", Addr: loc, Folders: []Folder{{Path: "0", Label: "Everything"}}}
		}
	}
	var out []Host
	for _, id := range slices.Sorted(maps.Keys(hosts)) {
		out = append(out, hosts[id])
	}
	return out
}

// selected is the set of folder IDs that should be in the library.
// selected maps each chosen folder ID to the content kinds it indexes.
func (s Settings) selected() map[string]map[string]bool {
	set := map[string]map[string]bool{}
	for _, h := range s.hostList() {
		for _, f := range h.Folders {
			set[folderID(hostID(h.Kind, h.Addr), f.Path)] = f.kindSet()
		}
	}
	return set
}

// migrateSettings converts the old per-server model (whole servers or shares,
// on/off switches) into hosts with chosen folders. Whole home directories and
// discovered servers aren't carried over: the user picks folders again.
func migrateSettings(s *Settings) bool {
	if len(s.DLNA)+len(s.SMB)+len(s.WebDAV)+len(s.SFTP)+len(s.Local)+len(s.Allowed) == 0 {
		return false
	}
	if s.Hosts == nil {
		s.Hosts = map[string]Host{}
	}
	add := func(kind, addr, name string, l Login, f *Folder) {
		id := hostID(kind, addr)
		h := s.Hosts[id]
		h.Kind, h.Addr, h.Login = kind, addr, l
		if h.Name == "" {
			h.Name = name
		}
		if f != nil && !slices.ContainsFunc(h.Folders, func(x Folder) bool { return x.Path == f.Path }) {
			h.Folders = append(h.Folders, *f)
		}
		s.Hosts[id] = h
	}
	for _, d := range s.Local {
		if !s.Disabled["local:"+d] {
			add("local", "", "This machine", Login{}, &Folder{Path: d, Label: d})
		}
	}
	for _, loc := range s.DLNA {
		if !s.Disabled["dlna:"+loc] {
			add("dlna", loc, "", Login{}, &Folder{Path: "0", Label: "Everything"})
		}
	}
	for root, l := range s.WebDAV {
		if !s.Disabled["webdav:"+root] {
			add("webdav", root, "", l, &Folder{Path: "", Label: "/"})
		}
	}
	for a, l := range s.SFTP {
		u, err := url.Parse(a)
		if err != nil {
			continue
		}
		p := u.Path
		u.Path = ""
		var f *Folder
		if p != "" && p != "/" && !s.Disabled["sftp:"+a] { // a whole home dir isn't kept
			f = &Folder{Path: p, Label: p}
		}
		add("sftp", u.String(), "", l, f)
	}
	for h, l := range s.SMB {
		add("smb", h, "", l, nil)
	}
	for id := range s.Allowed { // shares switched on under the old model
		if rest, ok := strings.CutPrefix(id, "smb://"); ok {
			if host, share, ok := strings.Cut(rest, "/"); ok {
				add("smb", host, "", s.SMB[host], &Folder{Path: share, Label: share})
			}
		}
	}
	s.DLNA, s.SMB, s.WebDAV, s.SFTP, s.Local, s.Allowed, s.Disabled = nil, nil, nil, nil, nil, nil, nil
	return true
}

// --- discovery: lists servers, never scans them ---

func discoverHosts(ctx context.Context) []Discovered {
	var out []Discovered
	if len(cfg.Servers) == 0 && cfg.DLNADiscover {
		locs, err := Discover(ctx, 3*time.Second)
		if err != nil {
			log.Printf("ssdp: %v", err)
		}
		for _, loc := range locs {
			d := Discovered{ID: hostID("dlna", loc), Kind: "dlna", Addr: loc, Name: loc}
			if srv, err := Describe(loc); err == nil {
				d.Name = srv.Name
			}
			out = append(out, d)
		}
	}
	for _, h := range discoverSMB(ctx) {
		out = append(out, Discovered{ID: hostID("smb", h), Kind: "smb", Addr: h, Name: smbName(h)})
	}
	return out
}

// --- scanning: only chosen folders ---

// scanHost scans the host's folders (or just only, if given), appending one
// Source per folder. A connection failure marks each of its folders.
func (idx *Index) scanHost(h Host, only *Folder, seen map[string]bool) {
	folders := h.Folders
	if only != nil {
		folders = []Folder{*only}
	}
	if len(folders) == 0 {
		return
	}
	hid := hostID(h.Kind, h.Addr)
	start := time.Now()
	label := h.Name
	if label == "" {
		label = h.Addr
	}
	if label == "" { // local host
		label = "This machine"
	}
	// connect once per host
	var (
		connErr error
		dlna    *Server
		smb     *smbConn
		sf      *sftpConn
		base    *url.URL
	)
	switch h.Kind {
	case "dlna":
		dlna, connErr = Describe(h.Addr)
		if connErr == nil && h.Name == "" {
			label = dlna.Name
		}
	case "smb":
		if smb, connErr = smbDialLogin(h.Addr, h.Login); connErr == nil {
			defer smb.Close()
		}
	case "sftp":
		if sf, _, connErr = sftpDial(h.Addr, h.Login, knownKey(h.Login)); connErr == nil {
			defer sf.Close()
		}
	case "webdav":
		base, connErr = url.Parse(h.Addr)
	}

	for _, f := range folders {
		fid := folderID(hid, f.Path)
		src := Source{ID: fid, Host: hid, Kind: h.Kind, Name: f.Label, Addr: f.Path}
		if src.Name == "" {
			src.Name = f.Path
		}
		n0 := len(idx.All)
		idx.cur, idx.kinds = fid, f.kindSet()
		if connErr != nil {
			src.Error = connErr.Error()
		} else {
			var err error
			switch h.Kind {
			case "local":
				err = idx.walkLocal(f.Path, label)
			case "dlna":
				err = idx.walkDLNA(dlna, f.Path, seen)
			case "smb":
				share, sub := splitShare(f.Path)
				var fs *smb2.Share
				if fs, err = smb.Mount(share); err == nil {
					if _, err = fs.Stat(sub); err == nil {
						idx.walkShare(fs, h.Addr, share, label, sub, 0)
					}
					fs.Umount()
				}
			case "sftp":
				err = idx.walkSFTP(sf, h.Addr, label, f.Path)
			case "webdav":
				err = idx.walkWebDAV(base.JoinPath(f.Path), h.Addr, label, h.Login)
			}
			if err != nil {
				src.Error = err.Error()
			}
		}
		src.Items = len(idx.All) - n0
		idx.Sources = append(idx.Sources, src)
		logSource(src, start)
	}
}

// splitShare turns "share/dir/sub" into ("share", "dir/sub"); "." for the share root.
func splitShare(p string) (string, string) {
	share, sub, _ := strings.Cut(strings.Trim(p, "/"), "/")
	if sub == "" {
		sub = "."
	}
	return share, sub
}

func (idx *Index) walkDLNA(srv *Server, root string, seen map[string]bool) error {
	visited := map[string]bool{}
	var firstErr error
	var walk func(id, title string, depth int)
	walk = func(id, title string, depth int) {
		if depth > cfg.MaxDepth || visited[id] {
			return
		}
		visited[id] = true
		d, err := srv.Browse(id)
		if err != nil {
			log.Printf("dlna %s: %v", srv.Name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		if d == nil {
			return
		}
		for i := range d.Items {
			di := &d.Items[i]
			audio := strings.HasPrefix(di.Class, "object.item.audioItem")
			if !audio && !strings.HasPrefix(di.Class, "object.item.videoItem") {
				continue
			}
			r := di.bestRes()
			if r == nil || seen[r.URL] {
				continue // Plex/Jellyfin expose the same file under many virtual folders
			}
			seen[r.URL] = true
			it := dlnaItem(srv, di, r)
			it.Album = title
			if audio {
				it.Kind = "audio"
			}
			if idx.kinds[itemKind(it)] {
				idx.add(it)
			}
		}
		for _, c := range d.Containers {
			walk(c.ID, c.Title, depth+1)
		}
	}
	walk(root, srv.Name, 0)
	return firstErr
}

func (idx *Index) walkSFTP(c *sftpConn, addr, label, root string) error {
	var rootErr error
	visited := map[string]bool{} // real paths: a link back up can't loop
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > cfg.MaxDepth {
			return
		}
		if real, err := c.sftp.RealPath(dir); err == nil {
			if visited[real] {
				return
			}
			visited[real] = true
		}
		infos, err := c.sftp.ReadDir(dir)
		if err != nil {
			debugf("sftp %s: %v", dir, err)
			if depth == 0 {
				rootErr = err
			}
			return
		}
		for _, fi := range infos {
			name := fi.Name()
			p := path.Join(dir, name)
			if strings.HasPrefix(name, ".") {
				continue
			}
			if fi.Mode()&os.ModeSymlink != 0 { // follow linked folders (mounted drives); linked files stay out
				if t, err := c.sftp.Stat(p); err == nil && t.IsDir() {
					fi = t
				}
			}
			if fi.IsDir() {
				if !skipDir(path.Base(dir), name) {
					walk(p, depth+1)
				}
				continue
			}
			if !fi.Mode().IsRegular() || !mediaFile(name, fi.Size()) {
				continue
			}
			idx.addFile(&Item{
				Key: key("sftp:" + addr + p), Server: label,
				Title: strings.TrimSuffix(name, path.Ext(name)), FileName: name,
				SFTPAddr: addr, Path: p, Size: fi.Size(),
			}, func() ([]byte, error) { return readLimited(c.sftp.Open(p)) })
		}
	}
	walk(root, 0)
	return rootErr
}

// reuseDAVFolder, in a scheduled scan, copies the last scan's files under an
// unchanged WebDAV folder (same stamp), and its subfolders' stamps, instead of
// opening it. Servers differ on whether deep changes bubble up to a folder's
// stamp, so user-triggered rescans never take this shortcut.
func (idx *Index) reuseDAVFolder(dirKey, dirPath, stamp string) bool {
	p := idx.prev
	if !idx.incremental || p == nil || p.DAVStamps[dirKey] != stamp {
		return false
	}
	for k, v := range p.DAVStamps {
		if strings.HasPrefix(k, dirKey) {
			idx.DAVStamps[k] = v
		}
	}
	for _, it := range p.All {
		if it.SourceID != idx.cur || it.DAVRoot == "" || idx.Items[it.Key] != nil {
			continue
		}
		if u, err := url.Parse(it.URL); err == nil && strings.HasPrefix(u.Path, dirPath) {
			c := *it
			idx.add(&c)
		}
	}
	return true
}

func (idx *Index) walkWebDAV(root *url.URL, base, label string, l Login) error {
	visited := map[string]bool{}
	var rootErr error
	var walk func(u *url.URL, depth int)
	walk = func(u *url.URL, depth int) {
		if depth > cfg.MaxDepth || visited[u.Path] {
			return
		}
		visited[u.Path] = true
		entries, err := propfind(u, l)
		if err != nil {
			log.Printf("webdav: %v", err)
			if depth == 0 {
				rootErr = err
			}
			return
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name, ".") {
				continue
			}
			if e.Dir {
				if skipDir(path.Base(strings.TrimSuffix(u.Path, "/")), e.Name) {
					continue
				}
				dirKey := base + e.URL.Path
				if e.Stamp != "" {
					idx.DAVStamps[dirKey] = e.Stamp
					if idx.reuseDAVFolder(dirKey, e.URL.Path, e.Stamp) {
						continue // unchanged since the last scan: its files were reused
					}
				}
				walk(e.URL, depth+1)
				continue
			}
			if !mediaFile(e.Name, e.Size) {
				continue
			}
			fileURL := e.URL
			idx.addFile(&Item{
				Key: key("webdav:" + base + e.URL.Path), Server: label,
				Title: strings.TrimSuffix(e.Name, path.Ext(e.Name)), FileName: e.Name,
				URL: e.URL.String(), DAVRoot: base, Size: e.Size,
			}, func() ([]byte, error) { return davGet(fileURL, l) })
		}
	}
	if !strings.HasSuffix(root.Path, "/") {
		root.Path += "/"
	}
	walk(root, 0)
	return rootErr
}

// --- explorer ---

type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Listing struct {
	Path    string  `json:"path"`    // the folder listed (resolved, e.g. SFTP home)
	Entries []Entry `json:"entries"` // subfolders
	Videos  int     `json:"videos"`  // video files directly in this folder
}

// browse lists one level of a host's folders for the explorer. p "" is the
// host's top level: shortcuts on this machine, shares on SMB, the home folder
// on SFTP, the base URL on WebDAV, the root container on DLNA.
func browse(h Host, p string, trustNewKey bool) (Listing, error) {
	out := Listing{Path: p, Entries: []Entry{}}
	switch h.Kind {
	case "local":
		if p == "" { // the whole filesystem: "/" (drive letters on Windows)
			if runtime.GOOS == "windows" {
				for c := 'C'; c <= 'Z'; c++ {
					if fi, err := os.Stat(string(c) + `:\`); err == nil && fi.IsDir() {
						out.Entries = append(out.Entries, Entry{Name: string(c) + ":", Path: string(c) + `:\`})
					}
				}
				return out, nil
			}
			p = "/"
		}
		if !filepath.IsAbs(p) {
			return out, errBadPath
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return out, err
		}
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(p, name)
			isDir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 { // a linked folder (often a mounted drive) is a folder too
				fi, err := os.Stat(full)
				isDir = err == nil && fi.IsDir()
			}
			if isDir {
				out.Entries = append(out.Entries, Entry{Name: name, Path: full})
			} else if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && mediaFile(name, fi.Size()) {
				out.Videos++
			}
		}
	case "smb":
		c, err := smbDialLogin(h.Addr, h.Login)
		if err != nil {
			return out, err
		}
		defer c.Close()
		if p == "" {
			names, err := c.ListSharenames()
			if err != nil {
				return out, err
			}
			for _, n := range names {
				if !strings.HasSuffix(n, "$") {
					out.Entries = append(out.Entries, Entry{Name: n, Path: n})
				}
			}
			return out, nil
		}
		share, sub := splitShare(p)
		fs, err := c.Mount(share)
		if err != nil {
			return out, err
		}
		defer fs.Umount()
		infos, err := fs.ReadDir(sub)
		if err != nil {
			return out, err
		}
		for _, fi := range infos {
			name := fi.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if fi.IsDir() {
				out.Entries = append(out.Entries, Entry{Name: name, Path: path.Join(share, strings.TrimPrefix(path.Join(sub, name), "./"))})
			} else if mediaFile(name, fi.Size()) {
				out.Videos++
			}
		}
	case "sftp":
		hk := knownKey(h.Login)
		if trustNewKey { // new server in the wizard; the key is pinned on save
			hk = ssh.InsecureIgnoreHostKey()
		}
		c, _, err := sftpDial(h.Addr, h.Login, hk)
		if err != nil {
			return out, err
		}
		defer c.Close()
		if p == "" { // the whole filesystem the server lets this user see
			p = "/"
		}
		infos, err := c.sftp.ReadDir(p)
		if err != nil {
			return out, err
		}
		for _, fi := range infos {
			name := fi.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := path.Join(p, name)
			if fi.Mode()&os.ModeSymlink != 0 { // resolve linked folders
				if t, err := c.sftp.Stat(full); err == nil {
					fi = t
				}
			}
			if fi.IsDir() {
				out.Entries = append(out.Entries, Entry{Name: name, Path: full})
			} else if fi.Mode().IsRegular() && mediaFile(name, fi.Size()) {
				out.Videos++
			}
		}
	case "webdav":
		base, err := url.Parse(h.Addr)
		if err != nil {
			return out, err
		}
		u := base.JoinPath(p)
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		entries, err := propfind(u, h.Login)
		if err != nil {
			return out, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name, ".") {
				continue
			}
			if e.Dir {
				rel := strings.Trim(strings.TrimPrefix(e.URL.Path, strings.TrimSuffix(base.Path, "/")), "/")
				out.Entries = append(out.Entries, Entry{Name: e.Name, Path: rel})
			} else if mediaFile(e.Name, e.Size) {
				out.Videos++
			}
		}
	case "dlna":
		srv, err := Describe(h.Addr)
		if err != nil {
			return out, err
		}
		if p == "" {
			p = "0"
			out.Path = p
		}
		d, err := srv.Browse(p)
		if err != nil {
			return out, err
		}
		for _, c := range d.Containers {
			out.Entries = append(out.Entries, Entry{Name: c.Title, Path: c.ID})
		}
		for _, it := range d.Items {
			if strings.HasPrefix(it.Class, "object.item.videoItem") {
				out.Videos++
			}
		}
	default:
		return out, errBadKind
	}
	slices.SortFunc(out.Entries, func(a, b Entry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

func needID(w http.ResponseWriter, id string) bool {
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
	}
	return id != ""
}

var (
	errBadPath = errors.New("need a full path")
	errBadKind = errors.New("unknown server type")
)

// --- API: explorer and hosts ---

type hostReq struct {
	ID      string   `json:"id"` // existing host, else Kind+Addr
	Kind    string   `json:"kind"`
	Addr    string   `json:"addr"`
	Name    string   `json:"name"`
	User    string   `json:"user"`
	Pass    string   `json:"pass"`
	Path    string   `json:"path"`    // browse only
	Folders []Folder `json:"folders"` // save only
}

// resolve builds the Host a request refers to: a saved one (its login, unless
// new credentials are given) or a new one from kind+addr.
func (q hostReq) resolve() (Host, bool, error) {
	st := getSettings()
	if q.ID != "" {
		h, ok := st.Hosts[q.ID]
		if !ok {
			return Host{}, false, errors.New("unknown host")
		}
		if q.User != "" {
			h.User = q.User
		}
		if q.Pass != "" {
			h.Pass = q.Pass
		}
		return h, true, nil
	}
	addr, err := validAddr(q.Kind, q.Addr)
	if err != nil {
		return Host{}, false, err
	}
	h, ok := st.Hosts[hostID(q.Kind, addr)]
	h.Kind, h.Addr = q.Kind, addr
	if q.User != "" || !ok {
		h.User = q.User
	}
	if q.Pass != "" || !ok {
		h.Pass = q.Pass
	}
	return h, ok, nil
}

func hostRoutes(mux *http.ServeMux) {
	// One level of a host's folders, for the explorer.
	mux.HandleFunc("POST /api/browse", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var q hostReq
		if !readJSON(w, r, &q) {
			return
		}
		h, saved, err := q.resolve()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		trust := !saved || h.HostKey == "" // SFTP in the wizard: key pinned on save
		l, err := browse(h, q.Path, trust)
		if err != nil {
			debugf("browse %s %q: %v", hostID(h.Kind, h.Addr), q.Path, err)
			http.Error(w, "can't open: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, l, false)
	}))

	// Create or update a host and its chosen folders; scans just that host.
	mux.HandleFunc("POST /api/hosts", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var q hostReq
		if !readJSON(w, r, &q) {
			return
		}
		h, saved, err := q.resolve()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if q.Name != "" {
			h.Name = q.Name
		}
		h.Folders = nil
		for _, f := range q.Folders {
			if h.Kind == "local" {
				p, err := validFolder(f.Path)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				f.Path = p
			}
			f.Kinds = cleanKinds(f.Kinds)
			if !slices.ContainsFunc(h.Folders, func(x Folder) bool { return x.Path == f.Path }) {
				h.Folders = append(h.Folders, f)
			}
		}
		if h.Kind == "sftp" && (!saved || h.HostKey == "" || q.User != "" || q.Pass != "") {
			if h.User == "" {
				http.Error(w, "SFTP needs a user", http.StatusBadRequest)
				return
			}
			if h.HostKey, err = probeSFTP(h.Addr, h.Login); err != nil {
				http.Error(w, "can't connect: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		id := hostID(h.Kind, h.Addr)
		if err := saveSettings(func(s *Settings) { s.Hosts[id] = h }); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("host %s: saved with %d folders", id, len(h.Folders))
		hideNow() // folders unticked leave Stremio at once
		queue(id) // the rest are (re)scanned
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("POST /api/hosts/remove", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var q hostReq
		if !readJSON(w, r, &q) || !needID(w, q.ID) {
			return
		}
		saved(w, saveSettings(func(s *Settings) { delete(s.Hosts, q.ID) }))
		log.Printf("host %s: removed", q.ID)
		hideNow()
	}))

	// Desktop app: its WebKit window can't follow stremio:// links (it would
	// navigate away and go blank), so the page asks the app to open them.
	mux.HandleFunc("POST /api/open", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var q struct{ URL string }
		if !readJSON(w, r, &q) {
			return
		}
		if openExternal == nil {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(q.URL, "stremio://") {
			http.Error(w, "only stremio:// links", http.StatusBadRequest)
			return
		}
		if err := openExternal(q.URL); err != nil {
			http.Error(w, "couldn't open Stremio: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// Rescan a host, a folder ("host#path"), or just look for servers ("discover").
	mux.HandleFunc("POST /api/sources/rescan", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var q hostReq
		if !readJSON(w, r, &q) || !needID(w, q.ID) {
			return
		}
		queue(q.ID)
		w.WriteHeader(http.StatusAccepted)
	}))
}
