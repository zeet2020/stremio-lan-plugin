package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getetag/><d:getlastmodified/></d:prop></d:propfind>`

type davEntry struct {
	URL  *url.URL
	Dir  bool
	Size int64
	Name string
	// Stamp is the folder's ETag (else last-modified): if a scheduled scan
	// sees the same stamp as last time, the folder is reused unopened.
	Stamp string
}

// sameHost: a and b are the same server (scheme, host, port; "https://h" and
// "https://h:443" are the same).
func sameHost(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		return map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// propfind lists the direct children of a WebDAV collection.
func propfind(u *url.URL, l Login) ([]davEntry, error) {
	req, _ := http.NewRequest("PROPFIND", u.String(), strings.NewReader(propfindBody))
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", `application/xml; charset="utf-8"`)
	if l.User != "" {
		req.SetBasicAuth(l.User, l.Pass)
	}
	debugf("PROPFIND %s", u)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("PROPFIND %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	var ms struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				Prop struct {
					Collection *struct{} `xml:"resourcetype>collection"`
					Length     int64     `xml:"getcontentlength"`
					ETag       string    `xml:"getetag"`
					Modified   string    `xml:"getlastmodified"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("PROPFIND %s: %w", u.Redacted(), err)
	}
	var out []davEntry
	for _, r := range ms.Responses {
		h, err := u.Parse(strings.TrimSpace(r.Href)) // hrefs are usually absolute paths
		if err != nil || strings.TrimSuffix(h.Path, "/") == strings.TrimSuffix(u.Path, "/") {
			continue // the collection itself
		}
		if !sameHost(h, u) {
			continue // another server: the login would go there when scanned or played
		}
		e := davEntry{URL: h, Name: path.Base(h.Path)}
		for _, ps := range r.Propstat {
			e.Dir = e.Dir || ps.Prop.Collection != nil
			e.Size = max(e.Size, ps.Prop.Length)
			if e.Stamp == "" {
				if e.Stamp = strings.TrimSpace(ps.Prop.ETag); e.Stamp == "" && ps.Prop.Modified != "" {
					e.Stamp = "mod:" + strings.TrimSpace(ps.Prop.Modified)
				}
			}
		}
		out = append(out, e)
	}
	return out, nil
}
