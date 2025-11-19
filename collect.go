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
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
)

// evLine is used to serialize JSONL writes from concurrent subscriptions
type evLine struct{ id, line string }

func collectCmd(args []string) {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	dataDir := commonFlags(fs)
	pubkey := fs.String("pubkey", "", "your 64-hex pubkey to read kind-3 follows from")
	relaysCSV := fs.String("relays", "wss://relay.damus.io,wss://nos.lol,wss://nostr.wine,wss://relay.snort.social,wss://wot.brainstorm.social,wss://profiles.nostr1.com", "comma-separated relay URLs to query for kind-10002")
	followRelay := fs.String("follow-relay", "", "optional specific relay to query kind 3 (defaults to first in relays)")
	batchSize := fs.Int("batch-size", 50, "number of authors per 10002 REQ batch")
	timeoutSec := fs.Int("timeout", 12, "seconds to wait for REQ per relay/batch")
	parallel := fs.Int("parallel", 4, "number of relays to query in parallel for 10002")
	if err := fs.Parse(args); err != nil { panic(err) }

	if *pubkey == "" || !isHex64(strings.ToLower(*pubkey)) {
		fmt.Fprintln(os.Stderr, "--pubkey (64-hex) is required and must be valid hex")
		os.Exit(1)
	}

	dd := *dataDir
	if err := os.MkdirAll(dd, 0o755); err != nil { panic(err) }
	jsonlPath := filepath.Join(dd, "all_relay_lists.jsonl")
	followsPath := filepath.Join(dd, "follows_list.txt")

	relays := splitCSV(*relaysCSV)
	if len(relays) == 0 {
		fmt.Fprintln(os.Stderr, "no relays provided")
		os.Exit(1)
	}
	fr := *followRelay
	if fr == "" { fr = relays[0] }

	// 1) Fetch follows (kind 3)
	fmt.Println("Step 1: Fetching your follow list (kind 3)...")
	follows, err := fetchFollows(fr, *pubkey, time.Duration(*timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get follows from %s: %v\n", fr, err)
		os.Exit(1)
	}
	if len(follows) == 0 {
		fmt.Println("No follows found; nothing to do")
		_ = writeLines(followsPath, nil)
		os.Exit(0)
	}
	_ = writeLines(followsPath, follows)
	fmt.Printf("Found %d follows.\n", len(follows))

	// 2) Fetch kind 10002 relay-list events for follows in batches across relays
	fmt.Println("Step 2: Fetching kind 10002 relay lists for follows across relays...")
	ctx := context.Background()

	// Prepare output file for JSONL writes
	jsonlFile, err := os.Create(jsonlPath)
	if err != nil { panic(err) }
	defer jsonlFile.Close()
	jsonlW := bufio.NewWriter(jsonlFile)
	defer jsonlW.Flush()

	// channel to serialize JSONL writes and de-dup by event id
	writeCh := make(chan evLine, 1024)
	doneW := make(chan struct{})
	seen := make(map[string]struct{})
	var mu sync.Mutex
	go func() {
		for rec := range writeCh {
			mu.Lock()
			if _, ok := seen[rec.id]; !ok {
				seen[rec.id] = struct{}{}
				fmt.Fprintln(jsonlW, rec.line)
			}
			mu.Unlock()
		}
		jsonlW.Flush()
		close(doneW)
	}()

	// batching authors
	batches := chunk(follows, *batchSize)
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	for _, rurl := range relays {
		for _, authors := range batches {
			sem <- struct{}{}
			wg.Add(1)
			go func(url string, auths []string) {
				defer wg.Done()
				defer func() { <-sem }()
				_ = fetch10002(ctx, url, auths, time.Duration(*timeoutSec)*time.Second, writeCh)
			}(rurl, authors)
		}
	}
	wg.Wait()
	close(writeCh)
	<-doneW

	fmt.Println("Collection complete.")
	fmt.Printf(" - JSONL written: %s\n", jsonlPath)
	fmt.Printf(" - Follows written: %s\n", followsPath)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" { out = append(out, p) }
	}
	return out
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func fetchFollows(relayURL, pubkey string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil { return nil, err }
	defer r.Close()

	sub, err := r.Subscribe(ctx, nostr.Filters{
		nostr.Filter{
			Kinds:   []int{3},
			Authors: []string{strings.ToLower(pubkey)},
		},
	})
	if err != nil { return nil, err }
	defer sub.Unsub()

	var follows []string
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case ev := <-sub.Events:
			if ev == nil { continue }
			if ev.Kind != 3 { continue }
			// extract p-tags
			for _, t := range ev.Tags {
				if len(t) >= 2 && t[0] == "p" {
					pk := strings.ToLower(t[1])
					if isHex64(pk) {
						follows = append(follows, pk)
					}
				}
			}
			break loop
		}
	}
	// sort+unique
	sort.Strings(follows)
	uniq := make([]string, 0, len(follows))
	var last string
	for _, v := range follows {
		if v != last { uniq = append(uniq, v); last = v }
	}
	return uniq, nil
}

func fetch10002(ctx context.Context, relayURL string, authors []string, timeout time.Duration, out chan<- evLine) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := nostr.RelayConnect(cctx, relayURL)
	if err != nil { return err }
	defer r.Close()

	// NIP-01 REQ with kinds 10002 and batched authors
	// Validate and normalize authors to ensure all are 64-char hex.
	validAuthors := make([]string, 0, len(authors))
	for _, a := range authors {
		a = strings.ToLower(strings.TrimSpace(a))
		if isHex64(a) {
			validAuthors = append(validAuthors, a)
		}
	}
	if len(validAuthors) == 0 {
		return nil
	}
	f := nostr.Filters{
		nostr.Filter{
			Kinds:   []int{10002},
			Authors: validAuthors,
		},
	}
	sub, err := r.Subscribe(cctx, f)
	if err != nil { return err }
	defer sub.Unsub()

	for {
		select {
		case <-cctx.Done():
			return nil
		case ev := <-sub.Events:
			if ev == nil { continue }
			if ev.Kind != 10002 { continue }
			line := ev.String()
			out <- evLine{id: strings.ToLower(ev.ID), line: line}
		}
	}
}
