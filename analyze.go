package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Event struct {
	Kind      int        `json:"kind"`
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

type set map[string]struct{}

func (s set) add(v string)      { s[v] = struct{}{} }
func (s set) has(v string) bool { _, ok := s[v]; return ok }

func urlToHost(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimPrefix(u, "wss://")
	u = strings.TrimPrefix(u, "ws://")
	u = strings.TrimSuffix(u, "/")
	// strip any path/query/fragment
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	var out []string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out, s.Err()
}

func writeLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return w.Flush()
}

func analyzeCmd(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	dataDir := commonFlags(fs)
	inputJSONL := fs.String("input", "", "path to all_relay_lists.jsonl (default: data-dir/all_relay_lists.jsonl)")
	followsFile := fs.String("follows", "", "path to follows_list.txt (default: data-dir/follows_list.txt)")
	if err := fs.Parse(args); err != nil {
		panic(err)
	}

	dd := *dataDir
	if *inputJSONL == "" {
		*inputJSONL = filepath.Join(dd, "all_relay_lists.jsonl")
	}
	if *followsFile == "" {
		*followsFile = filepath.Join(dd, "follows_list.txt")
	}
	excludeFile := filepath.Join(dd, "outbox_exclude.txt")

	// Load excludes -> hosts set
	exHosts := set{}
	if lines, err := readLines(excludeFile); err == nil {
		for _, l := range lines {
			h := urlToHost(l)
			if h != "" {
				exHosts.add(h)
			}
		}
	}

	// Parse JSONL 10002 events
	in, err := os.Open(*inputJSONL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening %s: %v\n", *inputJSONL, err)
		os.Exit(1)
	}
	defer in.Close()

	// Build WRITE map only (outbox): relay->set(pubkey)
	writeMap := map[string]set{}

	s := bufio.NewScanner(in)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Kind != 10002 {
			continue
		}
		pk := strings.ToLower(ev.PubKey)
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "r" {
				url := normalizeURL(tag[1])
				if url == "" {
					continue
				}
				host := urlToHost(url)
				if exHosts.has(host) {
					continue
				}
				// If the URL points to an inbox endpoint, skip it and prefer a different URL for outbox
				if strings.Contains(url, "/inbox") {
					continue
				}
				mode := ""
				if len(tag) >= 3 {
					mode = strings.ToLower(tag[2])
				}
				// Outbox rules:
				// - mode=="write" => use url
				// - mode==""      => use url (legacy implies outbox)
				// - mode=="read"  => skip (inbox-only)
				if mode == "write" || mode == "" {
					if writeMap[url] == nil {
						writeMap[url] = set{}
					}
					writeMap[url].add(pk)
				}
			}
		}
	}
	if err := s.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
	}

	// Write pubkey_relays_map_write.txt (pubkey url pairs)
	var writePairs []string
	for url, users := range writeMap {
		for pk := range users {
			writePairs = append(writePairs, fmt.Sprintf("%s %s", pk, url))
		}
	}
	sort.Strings(writePairs)
	if err := writeLines(filepath.Join(dd, "pubkey_relays_map_write.txt"), writePairs); err != nil {
		panic(err)
	}
	// Canonical map for router now points to WRITE pairs
	if err := writeLines(filepath.Join(dd, "pubkey_relays_map.txt"), writePairs); err != nil {
		panic(err)
	}

	// Derive outbox relays from WRITE map (unique URLs by host; excludes already applied)
	outbox := uniqueByHost(writeMap)
	if len(outbox) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no outbox relays derived (write map empty)")
	}
	if err := writeLines(filepath.Join(dd, "outbox_relays.txt"), outbox); err != nil {
		panic(err)
	}

	fmt.Println("Analyze complete.")
	fmt.Printf(" - WRITE pairs: %d\n", len(writePairs))
	fmt.Printf(" - Outbox relays: %d\n", len(outbox))
}

func uniqueByHost(relayMap map[string]set) []string {
	have := set{}
	var out []string
	var urls []string
	for url := range relayMap {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	for _, url := range urls {
		h := urlToHost(url)
		if h == "" {
			continue
		}
		if have.has(h) {
			continue
		}
		have.add(h)
		out = append(out, url)
	}
	return out
}
