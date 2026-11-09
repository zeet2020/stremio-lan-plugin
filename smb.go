package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// discoverSMB returns SMB_SERVERS if set, otherwise every host on the local
// IPv4 subnets that accepts TCP on port 445.
// ponytail: sweeps only the /24 around each interface address; big networks need SMB_SERVERS.
func discoverSMB(ctx context.Context) []string {
	if len(cfg.SMBServers) > 0 || !cfg.SMBDiscover {
		return cfg.SMBServers
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Printf("smb discover: %v", err)
		return nil
	}
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		found []string
		sem   = make(chan struct{}, 64)
		seen  = map[string]bool{}
		d     = net.Dialer{Timeout: 500 * time.Millisecond}
	)
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || !n.IP.IsPrivate() {
			continue
		}
		base := n.IP.To4().Mask(net.CIDRMask(24, 32))
		for i := 1; i < 255; i++ {
			ip := net.IPv4(base[0], base[1], base[2], byte(i)).String()
			if seen[ip] {
				continue
			}
			seen[ip] = true
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "445")); err == nil {
					c.Close()
					mu.Lock()
					found = append(found, ip)
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	return found
}

func smbAddr(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "445")
}

type smbConn struct {
	net.Conn
	*smb2.Session
}

func (c *smbConn) Close() {
	c.Session.Logoff()
	c.Conn.Close()
}

// smbDial connects to host with its saved login.
func smbDial(host string) (*smbConn, error) { return smbDialLogin(host, hostLogin("smb", host)) }

// smbDialLogin logs in with l, else SMB_USER/SMB_PASS, else as guest (go-smb2
// doesn't support anonymous).
func smbDialLogin(host string, l Login) (*smbConn, error) {
	logins := [][2]string{{cfg.SMBUser, cfg.SMBPass}}
	if l.User != "" {
		logins = [][2]string{{l.User, l.Pass}}
	} else if cfg.SMBUser == "" {
		logins = [][2]string{{"Guest", ""}}
	}
	var lastErr error
	for _, l := range logins {
		conn, err := net.DialTimeout("tcp", smbAddr(host), 5*time.Second)
		if err != nil {
			return nil, err
		}
		d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: l[0], Password: l[1]}}
		s, err := d.Dial(conn)
		if err == nil {
			return &smbConn{conn, s}, nil
		}
		conn.Close()
		lastErr = err
	}
	return nil, lastErr
}

// smbName is the host's reverse-DNS name if any, e.g. "nas", else the address.
func smbName(host string) string {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	if names, err := net.LookupAddr(h); err == nil && len(names) > 0 {
		return strings.SplitN(strings.TrimSuffix(names[0], "."), ".", 2)[0]
	}
	return h
}

func (idx *Index) walkShare(fs *smb2.Share, host, share, label, dir string, depth int) {
	if depth > cfg.MaxDepth {
		return
	}
	infos, err := fs.ReadDir(dir)
	if err != nil {
		debugf("smb %s/%s/%s: %v", host, share, dir, err)
		return
	}
	for _, fi := range infos {
		name := fi.Name()
		p := path.Join(dir, name)
		if strings.HasPrefix(name, ".") {
			continue
		}
		if fi.IsDir() {
			if !skipDir(path.Base(dir), name) {
				idx.walkShare(fs, host, share, label, p, depth+1)
			}
			continue
		}
		if !mediaFile(name, fi.Size()) {
			continue
		}
		idx.addFile(&Item{
			Key: key("smb://" + host + "/" + share + "/" + p), Server: label,
			Title: strings.TrimSuffix(name, path.Ext(name)), FileName: name,
			SMBHost: host, SMBShare: share, Path: p, Size: fi.Size(),
		}, func() ([]byte, error) { return readLimited(fs.Open(p)) })
	}
}

// serveSMB streams an SMB file with Range support. One SMB session per request.
// ponytail: new session per request (players open a few per seek); pool per host if latency hurts.
func serveSMB(w http.ResponseWriter, r *http.Request, it *Item) {
	c, err := smbDial(it.SMBHost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer c.Close()
	fs, err := c.Mount(it.SMBShare)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer fs.Umount()
	f, err := fs.Open(it.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	debugf("smb serve %s range=%q", it.Key, r.Header.Get("Range"))
	http.ServeContent(w, r, it.FileName, time.Time{}, f)
}
