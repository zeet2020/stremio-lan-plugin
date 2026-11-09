package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP sources are "sftp://host[:port]/path", added in the UI. The host key is
// recorded when the source is added (trust on first use) and enforced after.

type sftpConn struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

func (c *sftpConn) Close() {
	c.sftp.Close()
	c.ssh.Close()
}

// sshAuth uses the password if given, else the Pi user's unencrypted ~/.ssh keys.
func sshAuth(l Login) []ssh.AuthMethod {
	if l.Pass != "" {
		return []ssh.AuthMethod{ssh.Password(l.Pass)}
	}
	home, _ := os.UserHomeDir()
	var signers []ssh.Signer
	for _, f := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", f)); err == nil {
			if s, err := ssh.ParsePrivateKey(b); err == nil {
				signers = append(signers, s)
			}
		}
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}
}

func sftpDial(addr string, l Login, hostKey ssh.HostKeyCallback) (*sftpConn, string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, "", err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "22")
	}
	c, err := ssh.Dial("tcp", host, &ssh.ClientConfig{
		User: l.User, Auth: sshAuth(l), HostKeyCallback: hostKey, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, "", err
	}
	s, err := sftp.NewClient(c)
	if err != nil {
		c.Close()
		return nil, "", err
	}
	root := u.Path
	if root == "" {
		root = "."
	}
	return &sftpConn{c, s}, root, nil
}

// knownKey enforces the host key saved when the source was added.
func knownKey(l Login) ssh.HostKeyCallback {
	b, err := base64.StdEncoding.DecodeString(l.HostKey)
	if k, perr := ssh.ParsePublicKey(b); err == nil && perr == nil {
		return ssh.FixedHostKey(k)
	}
	return func(string, net.Addr, ssh.PublicKey) error {
		return errors.New("no saved host key; remove and re-add this server")
	}
}

// probeSFTP checks a new source can log in and read its folder, and returns
// the host key to trust from now on.
func probeSFTP(addr string, l Login) (string, error) {
	var key ssh.PublicKey
	c, root, err := sftpDial(addr, l, func(_ string, _ net.Addr, k ssh.PublicKey) error {
		key = k
		return nil
	})
	if err != nil {
		return "", err
	}
	defer c.Close()
	if _, err := c.sftp.ReadDir(root); err != nil {
		return "", fmt.Errorf("%s: %w", root, err)
	}
	log.Printf("sftp %s: trusting host key %s", addr, ssh.FingerprintSHA256(key))
	return base64.StdEncoding.EncodeToString(key.Marshal()), nil
}

// serveSFTP streams a file with Range support. One SSH session per request.
// ponytail: handshake per request (~100ms on a Pi); pool clients per source if seeking feels slow.
func serveSFTP(w http.ResponseWriter, r *http.Request, it *Item) {
	l := hostLogin("sftp", it.SFTPAddr)
	c, _, err := sftpDial(it.SFTPAddr, l, knownKey(l))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer c.Close()
	f, err := c.sftp.Open(it.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	debugf("sftp serve %s range=%q", it.Key, r.Header.Get("Range"))
	http.ServeContent(w, r, it.FileName, time.Time{}, f)
}
