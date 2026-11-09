package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// --- SSDP discovery ---

const mSearch = "M-SEARCH * HTTP/1.1\r\n" +
	"HOST: 239.255.255.250:1900\r\n" +
	"MAN: \"ssdp:discover\"\r\n" +
	"MX: 2\r\n" +
	"ST: urn:schemas-upnp-org:device:MediaServer:1\r\n\r\n"

// Discover returns deduped LOCATION URLs of MediaServers answering M-SEARCH.
func Discover(ctx context.Context, timeout time.Duration) ([]string, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for i := 0; i < 3; i++ {
		if _, err := conn.WriteTo([]byte(mSearch), dst); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetReadDeadline(deadline)

	seen := map[string]bool{}
	var out []string
	buf := make([]byte, 8192)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break // deadline
		}
		r := textproto.NewReader(bufio.NewReader(bytes.NewReader(buf[:n])))
		if _, err := r.ReadLine(); err != nil {
			continue
		}
		hdr, _ := r.ReadMIMEHeader() // canonicalizes keys, so case-insensitive
		if loc := hdr.Get("Location"); loc != "" && !seen[loc] {
			seen[loc] = true
			out = append(out, loc)
		}
	}
	return out, nil
}

// --- Device description ---

type Server struct {
	Name, UDN, ControlURL, ServiceType string
}

type xmlDevice struct {
	FriendlyName string `xml:"friendlyName"`
	UDN          string `xml:"UDN"`
	Services     []struct {
		ServiceType string `xml:"serviceType"`
		ControlURL  string `xml:"controlURL"`
	} `xml:"serviceList>service"`
	Devices []xmlDevice `xml:"deviceList>device"`
}

func findCD(d *xmlDevice) (*xmlDevice, string, string) {
	for _, s := range d.Services {
		if strings.HasPrefix(s.ServiceType, "urn:schemas-upnp-org:service:ContentDirectory:") {
			return d, s.ServiceType, s.ControlURL
		}
	}
	for i := range d.Devices {
		if dev, st, cu := findCD(&d.Devices[i]); dev != nil {
			return dev, st, cu
		}
	}
	return nil, "", ""
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func Describe(location string) (*Server, error) {
	resp, err := httpClient.Get(location)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var root struct {
		URLBase string    `xml:"URLBase"`
		Device  xmlDevice `xml:"device"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&root); err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	dev, st, cu := findCD(&root.Device)
	if dev == nil {
		return nil, fmt.Errorf("%s: no ContentDirectory service", location)
	}
	base := location
	if root.URLBase != "" {
		base = root.URLBase
	}
	b, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	ctl, err := b.Parse(strings.TrimSpace(cu))
	if err != nil {
		return nil, err
	}
	name := root.Device.FriendlyName
	if name == "" {
		name = dev.FriendlyName
	}
	udn := root.Device.UDN
	if udn == "" {
		udn = location
	}
	return &Server{Name: name, UDN: udn, ControlURL: ctl.String(), ServiceType: st}, nil
}

// --- ContentDirectory Browse ---

type didlRes struct {
	ProtocolInfo string `xml:"protocolInfo,attr"`
	Size         int64  `xml:"size,attr"`
	Duration     string `xml:"duration,attr"`
	Resolution   string `xml:"resolution,attr"`
	URL          string `xml:",chardata"`
}

type didlItem struct {
	ID       string    `xml:"id,attr"`
	Title    string    `xml:"title"`
	Class    string    `xml:"class"`
	AlbumArt string    `xml:"albumArtURI"`
	Res      []didlRes `xml:"res"`
}

type didl struct {
	Containers []struct {
		ID    string `xml:"id,attr"`
		Title string `xml:"title"`
	} `xml:"container"`
	Items []didlItem `xml:"item"`
}

// bestRes prefers http-get video/* resources with the largest size (the original, not a transcode).
func (it *didlItem) bestRes() *didlRes {
	var best *didlRes
	score := func(r *didlRes) int64 {
		s := r.Size
		if strings.HasPrefix(r.ProtocolInfo, "http-get:*:video/") {
			s += 1 << 62
		}
		return s
	}
	for i := range it.Res {
		r := &it.Res[i]
		r.URL = strings.TrimSpace(r.URL)
		if r.URL == "" {
			continue
		}
		if best == nil || score(r) > score(best) {
			best = r
		}
	}
	return best
}

const browseTmpl = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:Browse xmlns:u="%s">
<ObjectID>%s</ObjectID>
<BrowseFlag>BrowseDirectChildren</BrowseFlag>
<Filter>*</Filter>
<StartingIndex>%d</StartingIndex>
<RequestedCount>200</RequestedCount>
<SortCriteria></SortCriteria>
</u:Browse>
</s:Body>
</s:Envelope>`

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (s *Server) browsePage(id string, start int) (*didl, int, int, error) {
	body := fmt.Sprintf(browseTmpl, s.ServiceType, xmlEscape(id), start)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		debugf("Browse %s id=%s start=%d", s.ControlURL, id, start)
		req, _ := http.NewRequest("POST", s.ControlURL, strings.NewReader(body))
		req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
		req.Header.Set("SOAPACTION", `"`+s.ServiceType+`#Browse"`)
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("browse %q: HTTP %d%s", id, resp.StatusCode, upnpFault(data))
			continue
		}
		var env struct {
			Result         string `xml:"Body>BrowseResponse>Result"`
			NumberReturned int    `xml:"Body>BrowseResponse>NumberReturned"`
			TotalMatches   int    `xml:"Body>BrowseResponse>TotalMatches"`
		}
		if err := xml.Unmarshal(data, &env); err != nil {
			return nil, 0, 0, fmt.Errorf("browse %s envelope: %w", id, err)
		}
		var d didl
		if env.Result != "" {
			// Result is DIDL-Lite escaped inside the SOAP envelope: second unmarshal.
			if err := xml.Unmarshal([]byte(env.Result), &d); err != nil {
				return nil, 0, 0, fmt.Errorf("browse %s didl: %w", id, err)
			}
		}
		return &d, env.NumberReturned, env.TotalMatches, nil
	}
	return nil, 0, 0, lastErr
}

// Browse returns all direct children of a container, paginating.
func (s *Server) Browse(id string) (*didl, error) {
	all := &didl{}
	for start := 0; ; {
		d, returned, total, err := s.browsePage(id, start)
		if err != nil {
			return all, err
		}
		n := len(d.Containers) + len(d.Items)
		all.Containers = append(all.Containers, d.Containers...)
		all.Items = append(all.Items, d.Items...)
		if returned == 0 {
			returned = n
		}
		start += returned
		// Some servers report TotalMatches=0; stop on an empty page instead.
		if returned == 0 || (total > 0 && start >= total) {
			return all, nil
		}
	}
}

// upnpFault extracts a SOAP fault's UPnP error ("701 No such object") so a
// failed Browse says why, not just "HTTP 500".
func upnpFault(body []byte) string {
	var f struct {
		Code string `xml:"Body>Fault>detail>UPnPError>errorCode"`
		Desc string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
	}
	if xml.Unmarshal(body, &f) != nil || f.Code == "" {
		return ""
	}
	return fmt.Sprintf(" (UPnP error %s %s)", f.Code, strings.TrimSpace(f.Desc))
}
