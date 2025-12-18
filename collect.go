package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
)

// eventLine represents a relay list event for serialized JSONL writes
type eventLine struct {
	id   string
	line string
}

// progressTracker tracks collection progress across goroutines
type progressTracker struct {
	eventsReceived atomic.Int64
	eventsWritten  atomic.Int64
	batchesTotal   int
	batchesDone    atomic.Int64
	relaysTotal    int
}

func collectCmd(args []string) {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	dataDir := commonFlags(fs)
	pubkey := fs.String("pubkey", "", "your 64-hex pubkey to read kind-3 follows from")
	relaysCSV := fs.String("relays", "wss://relay.damus.io,wss://nos.lol,wss://nostr.wine,wss://relay.snort.social,wss://wot.brainstorm.social,wss://profiles.nostr1.com", "comma-separated relay URLs to query for kind-10002")
	followRelay := fs.String("follow-relay", "", "optional specific relay to query kind 3 (defaults to first in relays)")
	batchSize := fs.Int("batch-size", 50, "number of authors per 10002 REQ batch")
	timeoutSec := fs.Int("timeout", 12, "seconds to wait for REQ per relay/batch")
	parallel := fs.Int("parallel", 4, "number of relays to query in parallel for 10002")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		os.Exit(1)
	}

	if *pubkey == "" || !isHex64(strings.ToLower(*pubkey)) {
		fmt.Fprintln(os.Stderr, "--pubkey (64-hex) is required and must be valid hex")
		os.Exit(1)
	}

	dataDirectory := *dataDir
	if err := os.MkdirAll(dataDirectory, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create data directory: %v\n", err)
		os.Exit(1)
	}
	jsonlPath := filepath.Join(dataDirectory, "all_relay_lists.jsonl")
	followsPath := filepath.Join(dataDirectory, "follows_list.txt")
	userRelayListPath := filepath.Join(dataDirectory, "user_relay_list.txt")
	userPubkeyPath := filepath.Join(dataDirectory, "user_pubkey.txt")
	followSetsDir := filepath.Join(dataDirectory, "follow_sets")

	relays := splitCSV(*relaysCSV)
	if len(relays) == 0 {
		fmt.Fprintln(os.Stderr, "no relays provided")
		os.Exit(1)
	}
	followRelayURL := *followRelay
	if followRelayURL == "" {
		followRelayURL = relays[0]
	}

	ctx := context.Background()
	timeout := time.Duration(*timeoutSec) * time.Second

	// Step 1: Fetch user's own relay list (kind 10002)
	fmt.Println("\n==> Step 1: Fetching your relay list (kind 10002)")
	fmt.Printf("    Connecting to %s...\n", followRelayURL)

	userRelays, err := fetchUserRelayList(ctx, followRelayURL, *pubkey, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to get your relay list from %s: %v\n", followRelayURL, err)
		// Continue anyway - not critical
	} else if len(userRelays) > 0 {
		if err := writeLines(userRelayListPath, userRelays); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write user relay list: %v\n", err)
		} else {
			fmt.Printf("    ✓ Found %d relays in your relay list\n", len(userRelays))
		}
	} else {
		fmt.Println("    ⚠ No relay list found for your pubkey")
	}

	// Step 2: Fetch follows (kind 3)
	fmt.Println("\n==> Step 2: Fetching your follow list (kind 3)")
	fmt.Printf("    Connecting to %s...\n", followRelayURL)

	follows, err := fetchFollows(ctx, followRelayURL, *pubkey, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get follows from %s: %v\n", followRelayURL, err)
		os.Exit(1)
	}
	fmt.Printf("    ✓ Found %d follows from kind 3\n", len(follows))

	// Step 2b: Fetch follow sets (kind 30000)
	fmt.Println("\n==> Step 2b: Fetching your follow sets (kind 30000)")
	fmt.Printf("    Connecting to %s...\n", followRelayURL)

	// Create follow_sets directory
	if err := os.MkdirAll(followSetsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create follow_sets directory: %v\n", err)
	} else {
		followSets, err := fetchAndSaveFollowSets(ctx, followRelayURL, *pubkey, timeout, followSetsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to get follow sets from %s: %v\n", followRelayURL, err)
		} else {
			fmt.Printf("    ✓ Saved %d follow sets to %s\n", len(followSets), followSetsDir)
			// Merge all follow sets into follows list
			for _, setPubkeys := range followSets {
				follows = append(follows, setPubkeys...)
			}
			follows = deduplicateAndSort(follows)
		}
	}

	if len(follows) == 0 {
		fmt.Println("    No follows found; nothing to do")
		if err := writeLines(followsPath, nil); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write follows file: %v\n", err)
		}
		os.Exit(0)
	}

	if err := writeLines(followsPath, follows); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write follows file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("    ✓ Total unique follows: %d\n", len(follows))

	// Save user pubkey for later use
	if err := writeLines(userPubkeyPath, []string{strings.ToLower(*pubkey)}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write user pubkey file: %v\n", err)
	}

	// Step 3: Fetch kind 10002 relay-list events for follows in batches across relays
	fmt.Println("\n==> Step 3: Fetching kind 10002 relay lists for follows")

	// Prepare output file for JSONL writes
	jsonlFile, err := os.Create(jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create JSONL file: %v\n", err)
		os.Exit(1)
	}
	defer jsonlFile.Close()
	jsonlWriter := bufio.NewWriter(jsonlFile)
	defer jsonlWriter.Flush()

	// Create batches and initialize progress tracking
	batches := chunkAuthors(follows, *batchSize)
	progress := &progressTracker{
		batchesTotal: len(batches),
		relaysTotal:  len(relays),
	}

	fmt.Printf("    Querying %d relays with %d batches of ~%d authors each\n",
		len(relays), len(batches), *batchSize)
	fmt.Printf("    Parallel workers: %d\n", *parallel)
	fmt.Println()

	// Channel to serialize JSONL writes and deduplicate by event ID
	eventChan := make(chan eventLine, 1024)
	writerDone := make(chan struct{})
	seenEvents := make(map[string]struct{})
	var seenMutex sync.Mutex

	// Start writer goroutine
	go func() {
		for event := range eventChan {
			progress.eventsReceived.Add(1)
			seenMutex.Lock()
			if _, exists := seenEvents[event.id]; !exists {
				seenEvents[event.id] = struct{}{}
				fmt.Fprintln(jsonlWriter, event.line)
				progress.eventsWritten.Add(1)
			}
			seenMutex.Unlock()
		}
		jsonlWriter.Flush()
		close(writerDone)
	}()

	// Start progress reporter
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				received := progress.eventsReceived.Load()
				written := progress.eventsWritten.Load()
				batchesDone := progress.batchesDone.Load()
				totalBatches := int64(progress.batchesTotal * progress.relaysTotal)
				pct := float64(batchesDone) / float64(totalBatches) * 100
				fmt.Printf("    Progress: %d/%d batches (%.1f%%) | Events: %d received, %d unique\n",
					batchesDone, totalBatches, pct, received, written)
			}
		}
	}()

	// Process relays with semaphore for parallelism control
	// Each relay gets one connection that handles all batches
	semaphore := make(chan struct{}, *parallel)
	var wg sync.WaitGroup

	for _, relayURL := range relays {
		semaphore <- struct{}{}
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			if err := fetchAllBatches(ctx, url, batches, timeout, eventChan, progress); err != nil {
				// Log errors but continue with other relays
				fmt.Fprintf(os.Stderr, "    ⚠ Error from %s: %v\n", url, err)
			}
		}(relayURL)
	}

	wg.Wait()
	close(eventChan)
	<-writerDone
	close(progressDone)

	// Final summary
	fmt.Println()
	fmt.Println("==> Collection complete")
	fmt.Printf("    ✓ Total events received: %d\n", progress.eventsReceived.Load())
	fmt.Printf("    ✓ Unique events written: %d\n", progress.eventsWritten.Load())
	fmt.Printf("    ✓ JSONL file: %s\n", jsonlPath)
	fmt.Printf("    ✓ Follows file: %s\n", followsPath)
	fmt.Printf("    ✓ User relay list: %s\n", userRelayListPath)
	fmt.Printf("    ✓ User pubkey: %s\n", userPubkeyPath)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fetchUserRelayList retrieves the user's own relay list (kind 10002) from a relay
func fetchUserRelayList(ctx context.Context, relayURL, pubkey string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}
	defer relay.Close()

	filters := nostr.Filters{
		nostr.Filter{
			Kinds:   []int{10002},
			Authors: []string{strings.ToLower(pubkey)},
			Limit:   1,
		},
	}

	subscription, err := relay.Subscribe(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	defer subscription.Unsub()

	var relays []string
	for {
		select {
		case <-ctx.Done():
			return deduplicateAndSort(relays), nil
		case <-subscription.EndOfStoredEvents:
			// Relay finished sending stored events
			return deduplicateAndSort(relays), nil
		case event := <-subscription.Events:
			if event == nil {
				continue
			}
			if event.Kind != 10002 {
				continue
			}
			// Extract relay URLs from r-tags
			for _, tag := range event.Tags {
				if len(tag) >= 2 && tag[0] == "r" {
					relayURL := strings.TrimSpace(tag[1])
					// Only include valid relay URLs (no query params, etc)
					if isValidRelayURL(relayURL) {
						relays = append(relays, relayURL)
					}
				}
			}
		}
	}
}

// fetchFollows retrieves the follow list (kind 3) for a given pubkey from a relay
func fetchFollows(ctx context.Context, relayURL, pubkey string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}
	defer relay.Close()

	filters := nostr.Filters{
		nostr.Filter{
			Kinds:   []int{3},
			Authors: []string{strings.ToLower(pubkey)},
		},
	}

	subscription, err := relay.Subscribe(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	defer subscription.Unsub()

	var follows []string
	for {
		select {
		case <-ctx.Done():
			return deduplicateAndSort(follows), nil
		case <-subscription.EndOfStoredEvents:
			// Relay finished sending stored events
			return deduplicateAndSort(follows), nil
		case event := <-subscription.Events:
			if event == nil {
				continue
			}
			if event.Kind != 3 {
				continue
			}
			// Extract p-tags (pubkeys being followed)
			for _, tag := range event.Tags {
				if len(tag) >= 2 && tag[0] == "p" {
					pubkeyHex := strings.ToLower(tag[1])
					if isHex64(pubkeyHex) {
						follows = append(follows, pubkeyHex)
					}
				}
			}
		}
	}
}

// followSet represents a kind 30000 follow set with its identifier and pubkeys
type followSet struct {
	dTag    string
	title   string
	pubkeys []string
}

// fetchAndSaveFollowSets retrieves follow sets (kind 30000) and saves each to a separate file
func fetchAndSaveFollowSets(ctx context.Context, relayURL, pubkey string, timeout time.Duration, outputDir string) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}
	defer relay.Close()

	filters := nostr.Filters{
		nostr.Filter{
			Kinds:   []int{30000},
			Authors: []string{strings.ToLower(pubkey)},
		},
	}

	subscription, err := relay.Subscribe(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	defer subscription.Unsub()

	sets := make(map[string]*followSet)
	for {
		select {
		case <-ctx.Done():
			return saveFollowSets(sets, outputDir)
		case <-subscription.EndOfStoredEvents:
			return saveFollowSets(sets, outputDir)
		case event := <-subscription.Events:
			if event == nil {
				continue
			}
			if event.Kind != 30000 {
				continue
			}

			// Extract d-tag identifier
			dTag := "unnamed"
			title := ""
			for _, tag := range event.Tags {
				if len(tag) >= 2 && tag[0] == "d" {
					dTag = sanitizeFilename(tag[1])
					if dTag == "" {
						dTag = "unnamed"
					}
				} else if len(tag) >= 2 && tag[0] == "title" {
					title = tag[1]
				}
			}

			// Initialize set if not exists
			if sets[dTag] == nil {
				sets[dTag] = &followSet{
					dTag:    dTag,
					title:   title,
					pubkeys: []string{},
				}
			}

			// Extract p-tags (pubkeys in follow sets)
			for _, tag := range event.Tags {
				if len(tag) >= 2 && tag[0] == "p" {
					pubkeyHex := strings.ToLower(tag[1])
					if isHex64(pubkeyHex) {
						sets[dTag].pubkeys = append(sets[dTag].pubkeys, pubkeyHex)
					}
				}
			}
		}
	}
}

// saveFollowSets writes each follow set to a separate file
func saveFollowSets(sets map[string]*followSet, outputDir string) (map[string][]string, error) {
	result := make(map[string][]string)
	usedFilenames := make(map[string]bool)

	for dTag, set := range sets {
		// Deduplicate and sort pubkeys
		set.pubkeys = deduplicateAndSort(set.pubkeys)

		if len(set.pubkeys) == 0 {
			continue
		}

		// Create filename from d-tag with collision detection
		baseFilename := fmt.Sprintf("follow_set_%s.txt", dTag)
		filename := baseFilename
		counter := 1

		// Handle filename collisions
		for usedFilenames[filename] {
			filename = fmt.Sprintf("follow_set_%s_%d.txt", dTag, counter)
			counter++
			if counter > 100 {
				return nil, fmt.Errorf("too many filename collisions for d-tag: %s", dTag)
			}
		}
		usedFilenames[filename] = true

		filePath := filepath.Join(outputDir, filename)

		// Security check: ensure filePath is within outputDir
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path for %s: %w", filename, err)
		}
		absDir, err := filepath.Abs(outputDir)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve output directory: %w", err)
		}
		if !strings.HasPrefix(absPath, absDir) {
			return nil, fmt.Errorf("security: attempted path traversal with d-tag: %s", set.dTag)
		}

		// Prepare file content with header
		lines := []string{}
		if set.title != "" {
			lines = append(lines, fmt.Sprintf("# %s", set.title))
		}
		lines = append(lines, fmt.Sprintf("# d-tag: %s", set.dTag))
		lines = append(lines, fmt.Sprintf("# pubkeys: %d", len(set.pubkeys)))
		lines = append(lines, "#")
		lines = append(lines, set.pubkeys...)

		if err := writeLines(filePath, lines); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", filename, err)
		}

		fmt.Printf("      - %s (%d pubkeys)\n", filename, len(set.pubkeys))
		result[dTag] = set.pubkeys
	}

	return result, nil
}

// sanitizeFilename removes or replaces characters that are unsafe for filenames
func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Convert to lowercase for consistency
	s = strings.ToLower(s)

	// Remove any path separators to prevent directory traversal
	s = strings.ReplaceAll(s, "..", "")
	s = strings.ReplaceAll(s, "./", "")
	s = strings.ReplaceAll(s, ".\\", "")

	// Replace unsafe characters with underscores
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"\t", "_",
		"\n", "_",
		"\r", "_",
	)
	s = replacer.Replace(s)

	// Strip non-ASCII and control characters
	var result strings.Builder
	for _, r := range s {
		if r >= 32 && r <= 126 {
			result.WriteRune(r)
		}
	}
	s = result.String()

	// Remove leading/trailing dots, dashes, underscores
	s = strings.Trim(s, ".-_")

	// Collapse multiple underscores
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}

	// Check for reserved names (Windows)
	reserved := map[string]bool{
		"con": true, "prn": true, "aux": true, "nul": true,
		"com1": true, "com2": true, "com3": true, "com4": true,
		"com5": true, "com6": true, "com7": true, "com8": true,
		"com9": true, "lpt1": true, "lpt2": true, "lpt3": true,
		"lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true,
		"lpt8": true, "lpt9": true,
	}
	if reserved[s] {
		s = "_" + s
	}

	// Limit length (leave room for collision suffix)
	if len(s) > 40 {
		s = s[:40]
	}

	// Ensure non-empty result
	if s == "" {
		s = "unnamed"
	}

	return s
}

// fetchAllBatches opens one connection to a relay and processes all batches sequentially
func fetchAllBatches(ctx context.Context, relayURL string, batches [][]string, timeout time.Duration,
	out chan<- eventLine, progress *progressTracker) error {

	// Connect once to the relay
	connectCtx, connectCancel := context.WithTimeout(ctx, timeout)
	defer connectCancel()

	relay, err := nostr.RelayConnect(connectCtx, relayURL)
	if err != nil {
		return fmt.Errorf("relay connect: %w", err)
	}
	defer relay.Close()

	// Process each batch with a new subscription on the same connection
	for batchIdx, authors := range batches {
		if err := fetchBatch(ctx, relay, relayURL, authors, batchIdx, timeout, out); err != nil {
			// Log error but continue with next batch
			fmt.Fprintf(os.Stderr, "    ⚠ Error from %s batch %d: %v\n", relayURL, batchIdx+1, err)
		}
		progress.batchesDone.Add(1)
	}

	return nil
}

// fetchBatch retrieves kind 10002 events for a batch of authors using an existing relay connection
func fetchBatch(ctx context.Context, relay *nostr.Relay, relayURL string, authors []string, batchIdx int,
	timeout time.Duration, out chan<- eventLine) error {

	// Validate and normalize authors to ensure all are 64-char hex
	validAuthors := make([]string, 0, len(authors))
	for _, author := range authors {
		author = strings.ToLower(strings.TrimSpace(author))
		if isHex64(author) {
			validAuthors = append(validAuthors, author)
		}
	}

	if len(validAuthors) == 0 {
		return nil
	}

	// Create a timeout context for this batch
	batchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	filters := nostr.Filters{
		nostr.Filter{
			Kinds:   []int{10002},
			Authors: validAuthors,
		},
	}

	subscription, err := relay.Subscribe(batchCtx, filters)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer subscription.Unsub()

	for {
		select {
		case <-batchCtx.Done():
			return nil
		case <-subscription.EndOfStoredEvents:
			// Relay finished sending stored events, exit early
			return nil
		case event := <-subscription.Events:
			if event == nil {
				continue
			}
			if event.Kind != 10002 {
				continue
			}
			line := event.String()
			out <- eventLine{
				id:   strings.ToLower(event.ID),
				line: line,
			}
		}
	}
}

// deduplicateAndSort removes duplicates and sorts a slice of strings
func deduplicateAndSort(items []string) []string {
	if len(items) == 0 {
		return nil
	}

	sort.Strings(items)
	unique := make([]string, 0, len(items))
	last := ""
	for _, item := range items {
		if item != last {
			unique = append(unique, item)
			last = item
		}
	}
	return unique
}

// chunkAuthors splits a slice of authors into batches of the specified size
func chunkAuthors(authors []string, batchSize int) [][]string {
	if batchSize <= 0 {
		return [][]string{authors}
	}

	var batches [][]string
	for i := 0; i < len(authors); i += batchSize {
		end := i + batchSize
		if end > len(authors) {
			end = len(authors)
		}
		batches = append(batches, authors[i:end])
	}
	return batches
}
