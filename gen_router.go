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

	"github.com/relaytools/feedbuilder/internal/relayurl"
)

type streamConfig struct {
	Name    string
	Dir     string // "down" or "up"
	Authors []string
	URLs    []string
	Kinds   string // raw JSON array or empty
	PTag    string // for #p filter (notifications)
}

// greedySelectAndAssignN selects relays greedily so that each author is assigned
// to up to 'replicas' distinct relays. It returns the selected relays and a mapping
// of relay -> assigned authors.
func greedySelectAndAssignN(relayAuthors map[string][]string, replicas int) ([]string, map[string][]string) {
	// remaining need per author
	need := make(map[string]int)
	// track which authors each relay covers for quick iteration
	for _, authors := range relayAuthors {
		for _, a := range authors {
			if need[a] == 0 {
				need[a] = replicas
			}
		}
	}
	selected := []string{}
	assigned := make(map[string][]string)
	// Also prevent duplicate assignment of same author to same relay
	assignedSet := make(map[string]map[string]struct{}) // relay -> set(author)

	// helper to count gain
	gainOf := func(relay string) int {
		cnt := 0
		for _, a := range relayAuthors[relay] {
			if need[a] > 0 {
				// avoid counting if already assigned to this relay
				if set, ok := assignedSet[relay]; ok {
					if _, has := set[a]; has {
						continue
					}
				}
				cnt++
			}
		}
		return cnt
	}

	// loop until no author needs more or no gain
	for {
		// check completion
		done := true
		for _, v := range need {
			if v > 0 {
				done = false
				break
			}
		}
		if done {
			break
		}

		bestRelay := ""
		bestGain := 0
		for relay := range relayAuthors {
			g := gainOf(relay)
			if g > bestGain {
				bestGain = g
				bestRelay = relay
			}
		}
		if bestGain == 0 || bestRelay == "" {
			break
		}

		// assign as many needing authors as possible to bestRelay
		for _, a := range relayAuthors[bestRelay] {
			if need[a] <= 0 {
				continue
			}
			if assignedSet[bestRelay] == nil {
				assignedSet[bestRelay] = make(map[string]struct{})
			}
			if _, has := assignedSet[bestRelay][a]; has {
				continue
			}
			assignedSet[bestRelay][a] = struct{}{}
			assigned[bestRelay] = append(assigned[bestRelay], a)
			need[a]--
		}
		selected = append(selected, bestRelay)
	}

	// normalize and sort authors per relay
	for r := range assigned {
		assigned[r] = uniqueSorted(assigned[r])
	}
	for i := range selected {
		if u, err := relayurl.New(selected[i]); err == nil {
			selected[i] = u.String()
		}
	}
	return selected, assigned
}

func genRouterCmd(args []string) {
	fs := flag.NewFlagSet("gen-router", flag.ExitOnError)
	dataDir := commonFlags(fs)
	output := fs.String("output", "./strfry-router.config", "output router config path")
	authorsPerStream := fs.Int("authors-per-stream", 50, "max authors per stream section")
	streamPrefix := fs.String("stream-prefix", "follows", "prefix for down streams")
	includeUnassigned := fs.Bool("include-unassigned", false, "add one stream querying all selected relays for any unassigned authors (rare)")
	replicas := fs.Int("replicas", 1, "number of distinct relays to assign each author to (>=1)")
	kindsJSON := fs.String("kinds-json", "", "JSON array for down streams kinds filter (e.g. [0,1,3])")
	onlineOnly := fs.Bool("online-only", false, "use only online relays from NIP-66 monitoring (requires analyze --check-monitors)")

	// Notification sync options
	includeNotifs := fs.Bool("include-notifs", false, "add streams for user notifications (your posts and mentions)")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		os.Exit(1)
	}

	dd := *dataDir
	// Inputs
	mapFile := filepath.Join(dd, "pubkey_relays_map.txt")
	if *onlineOnly {
		mapFile = filepath.Join(dd, "pubkey_relays_map_online.txt")
		fmt.Println("Using online-only relay map from NIP-66 monitoring")
	}
	followsFile := filepath.Join(dd, "follows_list.txt")
	userRelayListFile := filepath.Join(dd, "user_relay_list.txt")
	userPubkeyFile := filepath.Join(dd, "user_pubkey.txt")

	followsSet := loadSetMust(followsFile)
	// Build relay->authors from pubkey_relays_map
	relayAuthors := make(map[string][]string)
	{
		pairs := readLinesMust(mapFile)
		for _, line := range pairs {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pk := strings.ToLower(fields[0])
			rurlRaw := strings.Join(fields[1:], " ")
			u, err := relayurl.New(rurlRaw)
			if err != nil {
				continue
			}
			rurl := u.String()
			if _, ok := followsSet[pk]; !ok {
				continue
			}
			relayAuthors[rurl] = append(relayAuthors[rurl], pk)
		}
	}
	// dedupe and sort authors per relay
	for r := range relayAuthors {
		relayAuthors[r] = uniqueSorted(relayAuthors[r])
	}

	// Compute greedy optimal set from relayAuthors and assign authors to up to N replicas
	if *replicas < 1 {
		*replicas = 1
	}
	selected, assigned := greedySelectAndAssignN(relayAuthors, *replicas)

	var streams []streamConfig
	// Create per-relay down streams for selected relays with their assigned authors
	for _, relay := range selected {
		if u, err := relayurl.New(relay); err == nil {
			relay = u.String()
		}
		auths := assigned[relay]
		if len(auths) == 0 {
			continue
		}
		// Validate authors are 64-char hex and normalize to lowercase
		filtered := make([]string, 0, len(auths))
		for _, a := range auths {
			a = strings.ToLower(strings.TrimSpace(a))
			if isHex64(a) {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		chunks := chunk(filtered, *authorsPerStream)
		for i, chunkAuthors := range chunks {
			name := fmt.Sprintf("%s_%s_%d", *streamPrefix, safeName(relay), i+1)
			streams = append(streams, streamConfig{Name: name, Dir: "down", Authors: chunkAuthors, URLs: []string{relay}, Kinds: *kindsJSON})
		}
	}

	// Optionally include authors still needing replicas across all selected relays
	if *includeUnassigned {
		// Build a count of assigned replicas per author
		counts := make(map[string]int)
		for _, s := range streams {
			for _, a := range s.Authors {
				counts[a]++
			}
		}
		var needMore []string
		for a := range followsSet {
			if counts[a] < *replicas {
				needMore = append(needMore, a)
			}
		}
		needMore = uniqueSorted(needMore)
		if len(needMore) > 0 {
			// Validate authors and normalize
			filtered := make([]string, 0, len(needMore))
			for _, a := range needMore {
				a = strings.ToLower(strings.TrimSpace(a))
				if isHex64(a) {
					filtered = append(filtered, a)
				}
			}
			if len(filtered) == 0 {
				// nothing valid to add
			} else {
				chunks := chunk(filtered, *authorsPerStream)
				for i, ch := range chunks {
					name := fmt.Sprintf("%s_unassigned_%d", *streamPrefix, i+1)
					// Query across selected relays for any missed authors
					urls := make([]string, len(selected))
					copy(urls, selected)
					streams = append(streams, streamConfig{Name: name, Dir: "down", Authors: ch, URLs: urls, Kinds: *kindsJSON})
				}
			}
		}
	}

	// Add notification streams if requested
	if *includeNotifs {
		// Load user's pubkey from file
		userPubkeyLines := readLinesIfExists(userPubkeyFile)
		if len(userPubkeyLines) == 0 {
			fmt.Fprintf(os.Stderr, "error: no user pubkey found at %s\n", userPubkeyFile)
			fmt.Fprintln(os.Stderr, "hint: run 'collect' command first with --pubkey to save your pubkey")
			os.Exit(1)
		}
		pubkey := strings.ToLower(strings.TrimSpace(userPubkeyLines[0]))
		if !isHex64(pubkey) {
			fmt.Fprintf(os.Stderr, "error: invalid pubkey in %s: %s\n", userPubkeyFile, pubkey)
			os.Exit(1)
		}

		// Load user's relay list from file and filter out invalid URLs
		userRelaysRaw := readLinesIfExists(userRelayListFile)
		userRelays := make([]string, 0, len(userRelaysRaw))
		for _, relayLine := range userRelaysRaw {
			u, err := relayurl.New(relayLine)
			if err != nil {
				continue
			}
			userRelays = append(userRelays, u.String())
		}
		if len(userRelays) == 0 {
			fmt.Fprintf(os.Stderr, "warning: no user relay list found at %s, skipping notification streams\n", userRelayListFile)
			fmt.Fprintln(os.Stderr, "hint: run 'collect' command first with --pubkey to fetch your relay list")
		} else {
			fmt.Printf("Adding notification streams for pubkey %s using %d relays\n", pubkey, len(userRelays))

			// Add stream for notifications mentioning user (inbox)
			for _, relay := range userRelays {
				name := fmt.Sprintf("notifs_inbox_%s", safeName(relay))
				streams = append(streams, streamConfig{
					Name:    name,
					Dir:     "down",
					Authors: nil, // No authors filter for inbox
					URLs:    []string{relay},
					Kinds:   *kindsJSON,
					PTag:    pubkey, // Special field for #p filter
				})
			}
		}
	}

	// Write taocpp::config
	if err := writeRouterConfig(*output, streams); err != nil {
		fmt.Fprintf(os.Stderr, "error writing router config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s (%d streams)\n", *output, len(streams))
}

func readLinesMust(path string) []string {
	lines, err := readLines(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
		os.Exit(1)
	}
	for i := range lines {
		if u, err := relayurl.New(lines[i]); err == nil {
			lines[i] = u.String()
		}
	}
	return lines
}

func readLinesIfExists(path string) []string {
	lines, err := readLines(path)
	if err != nil {
		return nil
	}
	for i := range lines {
		if u, err := relayurl.New(lines[i]); err == nil {
			lines[i] = u.String()
		}
	}
	return lines
}

func loadSetMust(path string) map[string]struct{} {
	m := make(map[string]struct{})
	lines, err := readLines(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
		os.Exit(1)
	}
	for _, l := range lines {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" {
			m[l] = struct{}{}
		}
	}
	return m
}

func uniqueSorted(in []string) []string {
	m := make(map[string]struct{})
	for _, v := range in {
		m[v] = struct{}{}
	}
	var out []string
	for v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func chunk[T any](in []T, n int) [][]T {
	if n <= 0 || len(in) == 0 {
		return nil
	}
	var out [][]T
	for i := 0; i < len(in); i += n {
		j := i + n
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

func safeName(relay string) string {
	name := strings.TrimPrefix(relay, "wss://")
	name = strings.TrimPrefix(name, "ws://")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

func writeRouterConfig(path string, streams []streamConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "connectionTimeout = 20")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "streams {")
	for _, s := range streams {
		fmt.Fprintf(w, "  %s {\n", s.Name)
		fmt.Fprintf(w, "    dir = \"%s\"\n", s.Dir)
		if s.Dir == "down" && (len(s.Authors) > 0 || s.PTag != "") {
			filter := make(map[string]any)

			// Add authors filter if present
			if len(s.Authors) > 0 {
				filter["authors"] = s.Authors
			}

			// Add #p filter if present (for notifications)
			if s.PTag != "" {
				filter["#p"] = []string{s.PTag}
			}

			// Add kinds filter if specified
			if s.Kinds != "" {
				var kinds any
				if err := json.Unmarshal([]byte(s.Kinds), &kinds); err == nil {
					filter["kinds"] = kinds
				}
			}

			b, _ := json.Marshal(filter)
			fmt.Fprintf(w, "    filter = %s\n", string(b))
		} else if s.Dir == "up" && s.Kinds != "" {
			// Optional kinds filter for uploads
			fmt.Fprintf(w, "    filter = { \"kinds\": %s }\n", s.Kinds)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "    urls = [")
		for _, u := range s.URLs {
			fmt.Fprintf(w, "      \"%s\"\n", u)
		}
		fmt.Fprintln(w, "    ]")
		fmt.Fprintln(w, "  }")
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "}")
	return w.Flush()
}
