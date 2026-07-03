package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip77"
)

// backupCmd pulls the user's own authored events from the relays in their
// kind-10002 relay list into a target relay (their personal strfry).
//
// Strategy per source relay, best-effort:
//  1. NIP-77 negentropy sync (target relay is the local set; strfry speaks
//     negentropy natively, so re-syncs only transfer missing events).
//  2. Fallback: crude time-windowed REQ pagination when negentropy is
//     unsupported or fails.
//
// Dirty relay lists are expected: every relay gets a bounded timeout and
// failures are logged and skipped.
func backupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := commonFlags(fs)
	pubkey := fs.String("pubkey", "", "author pubkey (hex); defaults to <data-dir>/user_pubkey.txt")
	target := fs.String("target", "", "target relay websocket URL (required), e.g. ws://localhost:7777")
	relaysCSV := fs.String("relays", "", "CSV of source relays; defaults to <data-dir>/user_relay_list.txt")
	perRelayTimeout := fs.Duration("timeout", 90*time.Second, "per-source-relay time budget")
	sinceDays := fs.Int("since-days", 0, "scraper history cutoff in days (0 = all history)")
	maxPages := fs.Int("max-pages", 200, "scraper max REQ pages per relay")
	pageSize := fs.Int("page-size", 500, "scraper REQ limit per page")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *target == "" {
		fmt.Fprintln(os.Stderr, "--target is required")
		os.Exit(2)
	}

	pk := strings.TrimSpace(*pubkey)
	if pk == "" {
		b, err := os.ReadFile(filepath.Join(*dataDir, "user_pubkey.txt"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "no --pubkey and cannot read user_pubkey.txt:", err)
			os.Exit(2)
		}
		pk = strings.TrimSpace(string(b))
	}
	if !isHex64(pk) {
		fmt.Fprintln(os.Stderr, "invalid pubkey:", pk)
		os.Exit(2)
	}

	var sources []string
	if *relaysCSV != "" {
		sources = splitCSV(*relaysCSV)
	} else {
		path := filepath.Join(*dataDir, "user_relay_list.txt")
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot read %s: %v (run collect first or pass --relays)\n", path, err)
			os.Exit(2)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				sources = append(sources, line)
			}
		}
	}
	sources = deduplicateAndSort(sources)

	// go-nostr logs relay NOTICEs via the global std logger; busy relays
	// flood job logs with rate-limit notices, so drop them
	log.SetOutput(io.Discard)
	nostr.InfoLogger = log.New(io.Discard, "", 0)

	fmt.Printf("==> Backing up notes by %s… from %d relays to %s\n", pk[:12], len(sources), *target)

	rootCtx := context.Background()
	local, err := connectWithRetry(rootCtx, *target, 3, 20*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to target %s: %v\n", *target, err)
		os.Exit(1)
	}
	defer local.Close()

	filter := nostr.Filter{Authors: []string{pk}}
	baseline := countLocal(rootCtx, local, filter)
	fmt.Printf("    target already has %d events by author\n", baseline)

	synced, scraped, failed := 0, 0, 0
	for _, src := range sources {
		if !isValidRelayURL(src) {
			fmt.Printf("    SKIP %s: not a valid relay URL\n", src)
			failed++
			continue
		}

		ctx, cancel := context.WithTimeout(rootCtx, *perRelayTimeout)
		err := negSyncBounded(ctx, local, src, filter)
		cancel()
		if err == nil {
			fmt.Printf("    ✓ %s: negentropy sync ok\n", src)
			synced++
			continue
		}
		if isConnectError(err) {
			// the dial itself failed (dead host, 402 paid relay, auth wall);
			// a scrape would fail the same way
			fmt.Printf("    ✗ %s: unreachable (%v)\n", src, err)
			failed++
			continue
		}
		fmt.Printf("    negentropy failed for %s (%v), falling back to scrape\n", src, err)

		ctx, cancel = context.WithTimeout(rootCtx, *perRelayTimeout)
		n, err := scrapeAuthor(ctx, local, src, pk, *sinceDays, *maxPages, *pageSize)
		cancel()
		if err != nil {
			fmt.Printf("    ✗ %s: scrape failed after %d events: %v\n", src, n, err)
			failed++
			continue
		}
		fmt.Printf("    ✓ %s: scraped %d events\n", src, n)
		scraped++
	}

	total := countLocal(rootCtx, local, filter)
	// QuerySync counts are capped by the relay's max REQ limit (500 for strfry)
	fmt.Printf("==> Backup done: %d relays negentropy, %d scraped, %d failed; author events %s -> %s\n",
		synced, scraped, failed, capped(baseline), capped(total))
}

func capped(n int) string {
	if n >= 500 {
		return "500+"
	}
	return fmt.Sprintf("%d", n)
}

// negSyncBounded runs NegentropySync but guarantees the context deadline is
// honored even if the library blocks on a relay that never answers NEG-OPEN.
func negSyncBounded(ctx context.Context, local *nostr.Relay, src string, filter nostr.Filter) error {
	errCh := make(chan error, 1)
	go func() { errCh <- nip77.NegentropySync(ctx, local, src, filter, nip77.Down) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return fmt.Errorf("negentropy timed out")
	}
}

func isConnectError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "error opening websocket") ||
		strings.Contains(s, "failed to WebSocket dial") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host")
}

// connectWithRetry dials the target relay, retrying while it may still be
// starting up (fresh installs run this right after the pod is created).
func connectWithRetry(ctx context.Context, url string, attempts int, delay time.Duration) (*nostr.Relay, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		relay, err := nostr.RelayConnect(dialCtx, url)
		cancel()
		if err == nil {
			return relay, nil
		}
		lastErr = err
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return nil, lastErr
}

func countLocal(ctx context.Context, local *nostr.Relay, filter nostr.Filter) int {
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	evts, err := local.QuerySync(qctx, filter)
	if err != nil {
		return -1
	}
	return len(evts)
}

// scrapeAuthor walks backwards through time with plain REQs, publishing every
// event by the author into the local relay. Duplicate publishes are cheap
// no-ops for strfry.
func scrapeAuthor(ctx context.Context, local *nostr.Relay, src, pubkey string, sinceDays, maxPages, pageSize int) (int, error) {
	remote, err := nostr.RelayConnect(ctx, src)
	if err != nil {
		return 0, err
	}
	defer remote.Close()

	var cutoff nostr.Timestamp
	if sinceDays > 0 {
		cutoff = nostr.Timestamp(time.Now().AddDate(0, 0, -sinceDays).Unix())
	}

	copied := 0
	until := nostr.Now()
	for page := 0; page < maxPages; page++ {
		f := nostr.Filter{
			Authors: []string{pubkey},
			Until:   &until,
			Limit:   pageSize,
		}
		evts, err := remote.QuerySync(ctx, f)
		if err != nil {
			return copied, err
		}
		if len(evts) == 0 {
			return copied, nil
		}

		oldest := until
		for _, evt := range evts {
			if evt == nil {
				continue
			}
			if err := local.Publish(ctx, *evt); err == nil {
				copied++
			}
			if evt.CreatedAt < oldest {
				oldest = evt.CreatedAt
			}
		}

		fmt.Printf("      page %d: %d events (total %d)\n", page+1, len(evts), copied)

		if cutoff > 0 && oldest <= cutoff {
			return copied, nil
		}
		if oldest >= until {
			// no progress (all events share one timestamp); step back to avoid looping
			until = until - 1
		} else {
			until = oldest - 1
		}
	}
	return copied, nil
}
