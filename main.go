//go:build !desktop

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

// main runs the headless service (systemd, Docker, a terminal). The desktop
// app is desktop.go, built with -tags desktop.
func main() {
	scanOnly := flag.Bool("scan", false, "scan all sources, print video items, exit")
	verbose := flag.Bool("v", false, "verbose: log every request and each source's scan (same as LOG_LEVEL=debug)")
	flag.Parse()
	if *verbose {
		os.Setenv("LOG_LEVEL", "debug")
	}
	setup()

	if *scanOnly {
		idx := Build(context.Background())
		for _, s := range idx.Sources {
			fmt.Printf("folder: %-6s %-40s items=%d %s\n", s.Kind, s.ID, s.Items, s.Error)
		}
		for _, it := range idx.All {
			fmt.Printf("%-40s S%02dE%02d y%-4d %s\n  %s\n", it.Parsed.Title, it.Parsed.Season, it.Parsed.Episode, it.Parsed.Year, it.Title, it.URL+it.SMBHost+"/"+it.SMBShare+"/"+it.Path)
		}
		return
	}
	if _, err := serve(); err != nil {
		log.Fatal(err)
	}
	select {}
}
