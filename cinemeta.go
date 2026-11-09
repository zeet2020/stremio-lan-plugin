package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var cinemetaBase = "https://v3-cinemeta.strem.io"

type cmResult struct {
	Name    string
	Year    int
	ok      bool
	expires time.Time
}

var (
	cmMu     sync.Mutex
	cmCache  = map[string]cmResult{}
	cmClient = &http.Client{Timeout: 5 * time.Second}
	reYear4  = regexp.MustCompile(`\d{4}`)
)

// cinemeta returns name+year for an IMDb id. Misses are cached for 1h, hits for 24h.
func cinemeta(typ, tt string) (cmResult, bool) {
	key := typ + "/" + tt
	cmMu.Lock()
	c, hit := cmCache[key]
	cmMu.Unlock()
	if hit && time.Now().Before(c.expires) {
		return c, c.ok
	}

	c = cmResult{expires: time.Now().Add(time.Hour)}
	resp, err := cmClient.Get(fmt.Sprintf("%s/meta/%s/%s.json", cinemetaBase, typ, tt))
	if err == nil {
		var body struct {
			Meta struct {
				Name        string `json:"name"`
				ReleaseInfo string `json:"releaseInfo"`
				Year        any    `json:"year"`
			} `json:"meta"`
		}
		if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&body) == nil && body.Meta.Name != "" {
			c.Name, c.ok = body.Meta.Name, true
			c.expires = time.Now().Add(24 * time.Hour)
			if y := reYear4.FindString(body.Meta.ReleaseInfo + " " + fmt.Sprint(body.Meta.Year)); y != "" {
				c.Year, _ = strconv.Atoi(y)
			}
		}
		resp.Body.Close()
	}
	if err != nil {
		debugf("cinemeta %s: %v", key, err)
		c.expires = time.Now().Add(time.Minute) // network error: retry soon
	}
	cmMu.Lock()
	cmCache[key] = c
	cmMu.Unlock()
	return c, c.ok
}
