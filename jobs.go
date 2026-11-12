package main

import (
	"context"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Scans run one at a time on the scheduler goroutine: a full scan every
// RESCAN_MINUTES (counted from the last one, across restarts via library.db),
// a full scan on Rescan, one host or folder when it's added, changed, removed
// or rescanned from its row, and "discover" to just list servers on the
// network. Deselecting folders needs no scan.

var (
	jobs       = make(chan string, 64) // "" = full scan, "discover", a host ID or a folder ID
	scanning   atomic.Bool
	scanningID atomic.Value // string: source being rescanned alone, "" for full

	// publishMu serializes read-modify-write of the current index: a scan
	// finishing and a source being hidden can't undo each other.
	publishMu sync.Mutex
)

func queueRescan() { queue("") }

// scheduled marks the timer's scan, the only incremental one.
const scheduled = "\x00scheduled"

func queue(id string) {
	select {
	case jobs <- id:
	default:
		log.Printf("scan queue full; dropped %q", id)
	}
}

func scheduler() {
	scanningID.Store("")
	for {
		var id string
		if wait := time.Until(current.Load().Built.Add(cfg.Rescan)); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
				id = scheduled
			case id = <-jobs:
				t.Stop()
			}
		} else {
			id = scheduled // overdue (or nothing saved yet)
		}
		// Coalesce whatever else is queued: any full scan wins; else rescan
		// each distinct source once.
		pending := map[string]bool{id: true}
		for drained := false; !drained; {
			select {
			case more := <-jobs:
				pending[more] = true
			default:
				drained = true
			}
		}
		switch {
		case pending[""]: // Rescan button: open everything
			runScan("", func() *Index { return Build(context.Background()) })
			continue
		case pending[scheduled]: // timer: skip unchanged WebDAV folders
			runScan("", func() *Index { return build(context.Background(), true) })
			delete(pending, scheduled)
		}
		for _, sid := range slices.Sorted(maps.Keys(pending)) {
			runScan(sid, func() *Index { return rescanSource(sid) })
		}
	}
}

func runScan(id string, scan func() *Index) {
	scanningID.Store(id)
	scanning.Store(true)
	idx := scan()
	publish(idx)
	scanning.Store(false)
	scanningID.Store("")
	genThumbs()
}

// publish makes idx current (minus anything switched off meanwhile) and saves it.
func publish(idx *Index) {
	publishMu.Lock()
	defer publishMu.Unlock() // saved inside the lock too: an older index can't be written last
	idx = withoutHidden(idx, getSettings())
	current.Store(idx)
	if err := saveIndex(idx); err != nil {
		log.Printf("library db: save: %v", err)
	}
}

// hideNow applies a switch-off at once, from the current index: no scan.
func hideNow() {
	publish(current.Load())
}

// withoutHidden drops folders that are no longer chosen, and items of
// content kinds their folder no longer indexes.
func withoutHidden(idx *Index, st Settings) *Index {
	sel := st.selected()
	keep := func(it *Item) bool { return sel[it.SourceID][itemKind(it)] }
	changed := false
	for _, s := range idx.Sources {
		if sel[s.ID] == nil {
			changed = true
		}
	}
	for _, it := range idx.All {
		if !changed && !keep(it) {
			changed = true
		}
	}
	if !changed {
		return idx
	}
	out := newIndex()
	out.Built, out.Discovered, out.DAVStamps = idx.Built, idx.Discovered, idx.DAVStamps
	for _, s := range idx.Sources {
		if sel[s.ID] != nil {
			out.Sources = append(out.Sources, s)
		}
	}
	for _, it := range idx.All {
		if keep(it) {
			c := *it
			out.add(&c)
		}
	}
	out.finish()
	for i, s := range out.Sources { // counts after kind filtering
		n := 0
		for _, it := range out.All {
			if it.SourceID == s.ID {
				n++
			}
		}
		out.Sources[i].Items = n
	}
	return out
}

// rescanSource rescans one host ("smb://nas") or one of its folders
// ("smb://nas#Movies") and keeps everything else from the current index. A
// host or folder that's no longer chosen comes back empty, which removes it.
// "discover" only refreshes the list of servers on the network.
func rescanSource(id string) *Index {
	start := time.Now()
	prev := current.Load()
	if id == "discover" {
		idx := *prev
		idx.Discovered = discoverHosts(context.Background())
		log.Printf("discover: %d servers on the network in %s", len(idx.Discovered), time.Since(start).Round(time.Millisecond))
		return &idx
	}
	st := getSettings()
	host, folder, isFolder := strings.Cut(id, "#")
	part := newIndex()
	part.prev = prev // full rescan (user-triggered): never reuses folders
	for _, h := range st.hostList() {
		if hostID(h.Kind, h.Addr) != host {
			continue
		}
		if !isFolder {
			part.scanHost(h, nil, map[string]bool{})
		} else if i := slices.IndexFunc(h.Folders, func(f Folder) bool { return f.Path == folder }); i >= 0 {
			part.scanHost(h, &h.Folders[i], map[string]bool{})
		}
	}
	for i := range part.Sources {
		part.keepLastGood(prev, &part.Sources[i])
	}

	// merge: everything else from prev, then what was rescanned
	idx := newIndex()
	idx.Built, idx.Discovered = prev.Built, prev.Discovered // the full-scan schedule doesn't move
	maps.Copy(idx.DAVStamps, prev.DAVStamps)
	maps.Copy(idx.DAVStamps, part.DAVStamps)
	for _, s := range prev.Sources {
		if !inGroup(s.ID, id) {
			idx.Sources = append(idx.Sources, s)
		}
	}
	idx.Sources = append(idx.Sources, part.Sources...)
	for _, it := range prev.All {
		if !inGroup(it.SourceID, id) {
			c := *it
			idx.add(&c)
		}
	}
	for _, it := range part.All {
		if idx.Items[it.Key] == nil {
			idx.add(it)
		}
	}
	idx.finish()
	log.Printf("scan %s: %d folders, %d files in %s", id, len(part.Sources), len(part.All), time.Since(start).Round(time.Millisecond))
	return idx
}
