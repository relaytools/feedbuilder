package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/relaytools/feedbuilder/internal/relayurl"
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
	checkMonitors := fs.Bool("check-monitors", false, "query NIP-66 relay monitors for liveness data")
	monitorRelays := fs.String("monitor-relays", "wss://monitorlizard.nostr1.com", "comma-separated list of relays to query for NIP-66 events")
	monitorTimeout := fs.Int("monitor-timeout", 10, "timeout in seconds for querying monitor relays")
	inputJSONL := fs.String("input", "", "path to all_relay_lists.jsonl (default: data-dir/all_relay_lists.jsonl)")
	followsFile := fs.String("follows", "", "path to follows_list.txt (default: data-dir/follows_list.txt)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		os.Exit(1)
	}

	dd := *dataDir
	if *inputJSONL == "" {
		*inputJSONL = filepath.Join(dd, "all_relay_lists.jsonl")
	}
	if *followsFile == "" {
		*followsFile = filepath.Join(dd, "follows_list.txt")
	}
	excludeFile := filepath.Join(dd, "outbox_exclude.txt")
	followSetsDir := filepath.Join(dd, "follow_sets")

	// Merge follow sets from individual files if they exist
	if err := mergeFollowSets(followSetsDir, *followsFile); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to merge follow sets: %v\n", err)
	}

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
				u, err := relayurl.New(tag[1])
				if err != nil {
					continue
				}
				url := u.String()
				if exHosts.has(u.Host()) {
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

	// Optionally check relay monitors for liveness
	if *checkMonitors {
		fmt.Println("\n==> Checking NIP-66 relay monitors...")
		monitorRelayList := strings.Split(*monitorRelays, ",")
		for i := range monitorRelayList {
			monitorRelayList[i] = strings.TrimSpace(monitorRelayList[i])
		}

		// Collect all unique relay URLs we want to check
		allRelays := set{}
		for rawURL := range writeMap {
			u, err := relayurl.New(rawURL)
			if err != nil {
				continue
			}
			allRelays.add(u.String())
		}

		monitorData := fetchNIP66MonitorData(monitorRelayList, allRelays, time.Duration(*monitorTimeout)*time.Second)

		// Write monitoring report
		reportPath := filepath.Join(dd, "relay_monitor_report.txt")
		if err := writeMonitorReport(reportPath, monitorData); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write monitor report: %v\n", err)
		} else {
			fmt.Printf(" - Monitor report: %s\n", reportPath)
			fmt.Printf(" - Monitored relays: %d online, %d offline, %d unknown\n",
				countByStatus(monitorData, "online"),
				countByStatus(monitorData, "offline"),
				countByStatus(monitorData, "unknown"))
		}

		// Filter writePairs to only include online relays
		var filteredPairs []string
		onlineRelays := set{}
		for rawURL, info := range monitorData {
			if info.Status == "online" {
				u, err := relayurl.New(rawURL)
				if err != nil {
					continue
				}
				onlineRelays.add(u.String())
			}
		}

		for _, pair := range writePairs {
			fields := strings.Fields(pair)
			if len(fields) >= 2 {
				raw := strings.Join(fields[1:], " ")
				u, err := relayurl.New(raw)
				if err != nil {
					continue
				}
				relayURL := u.String()
				if onlineRelays.has(relayURL) {
					filteredPairs = append(filteredPairs, pair)
				}
			}
		}

		// Write filtered map for gen-router to use
		filteredMapPath := filepath.Join(dd, "pubkey_relays_map_online.txt")
		if err := writeLines(filteredMapPath, filteredPairs); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write filtered relay map: %v\n", err)
		} else {
			fmt.Printf(" - Filtered map (online only): %s\n", filteredMapPath)
			fmt.Printf(" - Filtered pairs: %d (from %d total)\n", len(filteredPairs), len(writePairs))
		}
	}
}

// RelayMonitorInfo holds NIP-66 monitoring data for a relay
type RelayMonitorInfo struct {
	URL          string
	Status       string // "online", "offline", "unknown"
	RTTOpen      int    // milliseconds
	RTTRead      int    // milliseconds
	RTTWrite     int    // milliseconds
	LastChecked  int64  // unix timestamp
	MonitorCount int    // number of monitors reporting
}

// MonitorInfo holds information about a NIP-66 monitor
type MonitorInfo struct {
	Pubkey    string
	Frequency int64 // seconds between checks
}

// fetchMonitorInfo queries for kind 10166 monitor announcements to get check frequencies
func fetchMonitorInfo(monitorRelays []string, timeout time.Duration) map[string]*MonitorInfo {
	monitors := make(map[string]*MonitorInfo)

	fmt.Println("    Fetching monitor announcements (kind 10166)...")

	for _, monitorRelay := range monitorRelays {
		if monitorRelay == "" {
			continue
		}

		fmt.Printf("      Querying %s for monitor info...\n", monitorRelay)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		relay, err := nostr.RelayConnect(ctx, monitorRelay)
		if err != nil {
			fmt.Fprintf(os.Stderr, "      ⚠ Failed to connect: %v\n", err)
			cancel()
			continue
		}

		// Query for kind 10166 monitor announcements
		filters := nostr.Filters{
			nostr.Filter{
				Kinds: []int{10166},
				Limit: 100,
			},
		}

		sub, err := relay.Subscribe(ctx, filters)
		if err != nil {
			fmt.Fprintf(os.Stderr, "      ⚠ Failed to subscribe: %v\n", err)
			relay.Close()
			cancel()
			continue
		}

		eventsReceived := 0
		// Collect monitor announcements
		for {
			select {
			case <-ctx.Done():
				sub.Unsub()
				relay.Close()
				cancel()
				goto nextRelay
			case <-sub.EndOfStoredEvents:
				sub.Unsub()
				relay.Close()
				cancel()
				goto nextRelay
			case event := <-sub.Events:
				if event == nil {
					continue
				}
				eventsReceived++

				// Extract frequency from tags
				var frequency int64 = 3600 // default 1 hour
				for _, tag := range event.Tags {
					if len(tag) >= 2 && tag[0] == "frequency" {
						if freq := parseInt(tag[1]); freq > 0 {
							frequency = int64(freq)
						}
						break
					}
				}

				monitors[event.PubKey] = &MonitorInfo{
					Pubkey:    event.PubKey,
					Frequency: frequency,
				}
				fmt.Printf("      ✓ Monitor %s... (checks every %ds)\n", event.PubKey[:16], frequency)
			}
		}
	nextRelay:
		fmt.Printf("      Received %d monitor announcements from %s\n", eventsReceived, monitorRelay)
	}

	return monitors
}

// fetchNIP66MonitorData queries monitor relays for kind 30166 events
func fetchNIP66MonitorData(monitorRelays []string, targetRelays set, timeout time.Duration) map[string]*RelayMonitorInfo {
	result := make(map[string]*RelayMonitorInfo)

	// Initialize all target relays as unknown
	for relay := range targetRelays {
		result[relay] = &RelayMonitorInfo{
			URL:    relay,
			Status: "unknown",
		}
	}

	// First, fetch monitor info (kind 10166) to get frequencies
	monitors := fetchMonitorInfo(monitorRelays, timeout)
	fmt.Printf("    Found %d monitors with frequency data\n", len(monitors))

	// Convert target relays to slice for filter
	// NIP-66 requires normalized URLs with trailing slashes in d-tags
	var dTags []string
	for relay := range targetRelays {
		normalized := relay
		if !strings.HasSuffix(normalized, "/") {
			normalized += "/"
		}
		dTags = append(dTags, normalized)
	}

	fmt.Printf("    Querying %d monitor relays for %d target relays...\n", len(monitorRelays), len(dTags))

	// Batch size for d-tags to avoid "filter too large" errors
	// Note: relay URLs can be long, so we use a small batch size
	const batchSize = 2

	for _, monitorRelay := range monitorRelays {
		if monitorRelay == "" {
			continue
		}

		// Each monitor relay gets its own timeout
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		fmt.Printf("    Connecting to %s...\n", monitorRelay)
		relay, err := nostr.RelayConnect(ctx, monitorRelay)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ⚠ Failed to connect to %s: %v\n", monitorRelay, err)
			cancel()
			continue
		}

		eventsReceived := 0

		// Process in batches to avoid filter size limits
		for i := 0; i < len(dTags); i += batchSize {
			end := i + batchSize
			if end > len(dTags) {
				end = len(dTags)
			}
			batch := dTags[i:end]

			// Query for kind 30166 events with d tags matching this batch
			// Only get events from the last 3 days to avoid stale data
			threeDaysAgo := nostr.Timestamp(time.Now().Add(-3 * 24 * time.Hour).Unix())
			filters := nostr.Filters{
				nostr.Filter{
					Kinds: []int{30166},
					Tags:  nostr.TagMap{"d": batch},
					Since: &threeDaysAgo,
					Limit: 500,
				},
			}

			// Debug: log filter details
			filterJSON, _ := json.Marshal(filters)
			fmt.Printf("    [DEBUG] Subscribing batch %d-%d, filter size: %d bytes\n", i, end, len(filterJSON))

			sub, err := relay.Subscribe(ctx, filters)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    ⚠ Failed to subscribe to %s (batch %d-%d): %v\n", monitorRelay, i, end, err)
				fmt.Fprintf(os.Stderr, "    [DEBUG] Filter was: %s\n", string(filterJSON))
				continue
			}

			// Collect events from this batch
		batchLoop:
			for {
				select {
				case <-ctx.Done():
					sub.Unsub()
					break batchLoop
				case <-sub.EndOfStoredEvents:
					sub.Unsub()
					break batchLoop
				case event := <-sub.Events:
					if event == nil {
						continue
					}
					eventsReceived++
					parseNIP66Event(event, result, monitors)
				}
			}
		}

		relay.Close()
		cancel()
		fmt.Printf("    ✓ Received %d monitor events from %s\n", eventsReceived, monitorRelay)
	}

	return result
}

// parseNIP66Event extracts monitoring data from a kind 30166 event
func parseNIP66Event(event *nostr.Event, result map[string]*RelayMonitorInfo, monitors map[string]*MonitorInfo) {
	var relayURL string

	// Extract d tag (relay URL)
	// NIP-66 d-tags have trailing slashes, but our stored URLs don't
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			u, err := relayurl.New(tag[1])
			if err != nil {
				continue
			}
			relayURL = u.String()
			break
		}
	}

	if relayURL == "" {
		return
	}

	info, exists := result[relayURL]
	if !exists {
		// This shouldn't happen since we initialized all target relays
		// but handle it gracefully
		info = &RelayMonitorInfo{
			URL:    relayURL,
			Status: "unknown",
		}
		result[relayURL] = info
	}

	eventTime := event.CreatedAt.Time().Unix()

	// Update last checked time if this event is newer
	if eventTime > info.LastChecked {
		info.LastChecked = eventTime
	}

	// Increment monitor count
	info.MonitorCount++

	// Get this monitor's check frequency (default to 1 hour if unknown)
	monitorFrequency := int64(3600) // default 1 hour
	monitorKnown := false
	if monitor, ok := monitors[event.PubKey]; ok {
		monitorFrequency = monitor.Frequency
		monitorKnown = true
	}

	// Check if this event is recent enough based on the monitor's frequency
	// Allow 2x the frequency as a grace period (e.g., if monitor checks hourly, allow 2 hours)
	freshnessWindow := monitorFrequency * 2
	cutoff := time.Now().Unix() - freshnessWindow
	isRecent := eventTime >= cutoff

	// Debug logging (only for first few relays to avoid spam)
	if info.MonitorCount <= 2 {
		age := time.Now().Unix() - eventTime
		fmt.Printf("      [%s] Monitor %s... freq=%ds window=%ds age=%ds recent=%v known=%v\n",
			relayURL[:min(30, len(relayURL))],
			event.PubKey[:8],
			monitorFrequency,
			freshnessWindow,
			age,
			isRecent,
			monitorKnown)
	}

	// Extract RTT values
	hasRTT := false
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "rtt-open":
			if val := parseInt(tag[1]); val > 0 {
				if info.RTTOpen == 0 || val < info.RTTOpen {
					info.RTTOpen = val
				}
				hasRTT = true
			}
		case "rtt-read":
			if val := parseInt(tag[1]); val > 0 {
				if info.RTTRead == 0 || val < info.RTTRead {
					info.RTTRead = val
				}
				hasRTT = true
			}
		case "rtt-write":
			if val := parseInt(tag[1]); val > 0 {
				if info.RTTWrite == 0 || val < info.RTTWrite {
					info.RTTWrite = val
				}
				hasRTT = true
			}
		}
	}

	// Mark as online only if:
	// 1. The monitor event is recent (within last 24 hours)
	// 2. AND it has RTT data (successful connection)
	if isRecent && hasRTT {
		info.Status = "online"
	}
}

func parseInt(s string) int {
	var val int
	fmt.Sscanf(s, "%d", &val)
	return val
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// writeMonitorReport writes the monitoring data to a file
func writeMonitorReport(path string, data map[string]*RelayMonitorInfo) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	// Sort by URL
	var urls []string
	for relayURL := range data {
		urls = append(urls, relayURL)
	}
	sort.Strings(urls)

	// Write header
	fmt.Fprintln(w, "# NIP-66 Relay Monitor Report")
	fmt.Fprintln(w, "# Format: URL | Status | RTT-Open | RTT-Read | RTT-Write | Monitors | Last-Checked")
	fmt.Fprintln(w, "")

	for _, relayURL := range urls {
		info := data[relayURL]
		lastChecked := "never"
		if info.LastChecked > 0 {
			lastChecked = time.Unix(info.LastChecked, 0).Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s | %s | %dms | %dms | %dms | %d | %s\n",
			info.URL,
			info.Status,
			info.RTTOpen,
			info.RTTRead,
			info.RTTWrite,
			info.MonitorCount,
			lastChecked)
	}

	return nil
}

// countByStatus counts relays by their status
func countByStatus(data map[string]*RelayMonitorInfo, status string) int {
	count := 0
	for _, info := range data {
		if info.Status == status {
			count++
		}
	}
	return count
}

func uniqueByHost(relayMap map[string]set) []string {
	have := set{}
	var out []string
	var urls []string
	for relay := range relayMap {
		urls = append(urls, relay)
	}
	sort.Strings(urls)
	for _, relay := range urls {
		h := urlToHost(relay)
		if h == "" {
			continue
		}
		if have.has(h) {
			continue
		}
		have.add(h)
		out = append(out, relay)
	}
	return out
}

// mergeFollowSets reads individual follow set files and merges them with the main follows list
func mergeFollowSets(followSetsDir, followsFile string) error {
	// Check if follow_sets directory exists
	if _, err := os.Stat(followSetsDir); os.IsNotExist(err) {
		return nil
	}

	// Read existing follows from follows_list.txt
	existingFollows := set{}
	if lines, err := readLines(followsFile); err == nil {
		for _, line := range lines {
			line = strings.ToLower(strings.TrimSpace(line))
			if line != "" && !strings.HasPrefix(line, "#") {
				existingFollows.add(line)
			}
		}
	}

	// Read all follow set files
	entries, err := os.ReadDir(followSetsDir)
	if err != nil {
		return fmt.Errorf("failed to read follow_sets directory: %w", err)
	}

	setsFound := 0
	pubkeysAdded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "follow_set_") || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}

		setPath := filepath.Join(followSetsDir, entry.Name())
		lines, err := readLines(setPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to read %s: %v\n", entry.Name(), err)
			continue
		}

		setsFound++
		for _, line := range lines {
			line = strings.ToLower(strings.TrimSpace(line))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !existingFollows.has(line) {
				existingFollows.add(line)
				pubkeysAdded++
			}
		}
	}

	if setsFound > 0 {
		fmt.Printf("Merged %d follow sets (%d new pubkeys) into follows list\n", setsFound, pubkeysAdded)

		// Write merged follows back to file
		var allFollows []string
		for pk := range existingFollows {
			allFollows = append(allFollows, pk)
		}
		sort.Strings(allFollows)

		if err := writeLines(followsFile, allFollows); err != nil {
			return fmt.Errorf("failed to write merged follows: %w", err)
		}
	}

	return nil
}
