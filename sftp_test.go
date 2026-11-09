package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpServer runs a read-only SFTP server on 127.0.0.1 accepting me/pw.
func sftpServer(t *testing.T) string {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewSignerFromKey(priv)
	conf := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "me" && string(pw) == "pw" {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	conf.AddHostKey(hk)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, conf)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, creqs, _ := nch.Accept()
					go func() {
						for req := range creqs {
							ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
							req.Reply(ok, nil)
							if ok {
								srv, _ := sftp.NewServer(ch, sftp.ReadOnly())
								srv.Serve()
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestSFTP(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Shows", "BB"), 0o755)
	os.WriteFile(filepath.Join(dir, "Shows", "BB", "Breaking.Bad.S01E02.mkv"), []byte("abcdefghij"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)

	cfg = Config{MaxDepth: 12}
	settings, settingsFile, db = Settings{}, "", nil
	initSecrets()
	tk := "/" + getSettings().AddonToken
	current.Store(newIndex())
	h := routes()
	drainJobs()
	server := sftpServer(t)
	host := "sftp:sftp://" + server
	conn := `"kind":"sftp","addr":"` + server + `","user":"me"`

	// explorer: wrong password can't open; right one lists the folder
	if code, body := post(t, h, "/api/browse", `{`+conn+`,"pass":"nope","path":"`+dir+`"}`); code != 502 {
		t.Errorf("wrong password → %d %s", code, body)
	}
	// top level: the whole filesystem, not just the home folder
	code, body := post(t, h, "/api/browse", `{`+conn+`,"pass":"pw","path":""}`)
	if code != 200 || !strings.Contains(body, `"path":"/tmp"`) { // top level is "/"
		t.Fatalf("browse top → %d %s", code, body)
	}
	code, body = post(t, h, "/api/browse", `{`+conn+`,"pass":"pw","path":"`+dir+`"}`)
	if code != 200 || !strings.Contains(body, `"name":"Shows"`) {
		t.Fatalf("browse → %d %s", code, body)
	}
	// save the Shows folder: host key pinned, scan queued for the host
	if code, body := post(t, h, "/api/hosts", `{`+conn+`,"pass":"pw","folders":[{"path":"`+filepath.Join(dir, "Shows")+`","label":"Shows"}]}`); code != 204 {
		t.Fatalf("save sftp → %d %s", code, body)
	}
	if getSettings().Hosts[host].HostKey == "" {
		t.Fatal("host key not recorded")
	}
	publish(rescanSource(<-jobs))
	idx := current.Load()
	if len(idx.All) != 1 || len(idx.Shows) != 1 {
		t.Fatalf("index: %d items, %d shows, sources %+v", len(idx.All), len(idx.Shows), idx.Sources)
	}
	urls := streamURLs(t, h, tk+"/stream/series/lan:s:breakingbad:1:2.json")
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "http://example.com"+tk+"/file/") {
		t.Fatalf("stream: %v", urls)
	}
	rec := get(t, h, strings.TrimPrefix(urls[0], "http://example.com"), "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "cde" {
		t.Errorf("sftp file: %d %q", rec.Code, rec.Body.String())
	}

	// A different key on the same address (server swapped / MITM) must be refused.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(other)
	saveSettings(func(s *Settings) {
		hh := s.Hosts[host]
		hh.HostKey = base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
		s.Hosts[host] = hh
	})
	// The scan fails; the last good listing stays (stale), but playback re-checks
	// the key on every request, so the swapped server serves nothing.
	bad := rescanSource(host)
	if src := bad.Sources[0]; !strings.Contains(src.Error, "host key") || !src.Stale || len(bad.All) != 1 {
		t.Fatalf("host key mismatch: %+v", bad.Sources)
	}
	current.Store(bad)
	if rec := get(t, h, strings.TrimPrefix(urls[0], "http://example.com")); rec.Code != 502 {
		t.Errorf("playback with mismatched host key → %d, want 502", rec.Code)
	}
	// saved host: browsing now checks the pinned (wrong) key too
	if code, _ := post(t, h, "/api/browse", `{"id":"`+host+`","path":"`+dir+`"}`); code != 502 {
		t.Errorf("browse with mismatched key → %d, want 502", code)
	}
	// re-entering the password doesn't re-trust the swapped server
	pinned := getSettings().Hosts[host].HostKey
	if code, body := post(t, h, "/api/hosts", `{"id":"`+host+`","pass":"pw","folders":[{"path":"`+dir+`"}]}`); code != 400 || !strings.Contains(body, "host key changed") {
		t.Errorf("re-save with changed key → %d %s", code, body)
	}
	if getSettings().Hosts[host].HostKey != pinned {
		t.Error("re-save replaced the pinned host key")
	}
}
