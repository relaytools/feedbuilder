# nostr-go-router

Go-based tools to analyze kind-10002 relay lists and generate a `strfry router` config.

Subcommands:

- `collect` — Fetch follows (kind 3) and relay lists (kind 10002) into data directory.
- `analyze` — Parse JSONL `10002` events, build READ/WRITE pubkey→relay maps, apply exclude hosts, compute optimal relay set (greedy), and derive outbox relays.
- `gen-router` — Generate a `strfry router` taocpp::config file using per-relay authors and the computed sets. Optionally generate notification sync commands.

Data directory (defaults to `relay_data/` next to where you run the command):

- `all_relay_lists.jsonl` — JSONL of kind-10002 events collected from follows.
- `follows_list.txt` — List of your follows (one 64-hex pubkey per line).
- `user_relay_list.txt` — Your own relay list (kind 10002) extracted as URLs, one per line.
- `user_pubkey.txt` — Your pubkey (saved by collect command).
- `outbox_exclude.txt` — Optional input list of relays you do NOT want to publish to (one URL per line).
- `pubkey_relays_map_read.txt` — Output; pubkey→relay mapping for read/REQ coverage.
- `pubkey_relays_map_write.txt` — Output; pubkey→relay mapping for outbox/write.
- `pubkey_relays_map.txt` — Output; canonical map used by gen-router (points to WRITE pairs).
- `pubkey_relays_map_online.txt` — Optional output; filtered map with only online relays (if `--check-monitors` used).
- `optimal_relay_set.txt` — Output; relays chosen by greedy set cover (from READ map, excludes honored).
- `outbox_relays.txt` — Output; relays for uploads derived from WRITE map, excludes honored.
- `relay_monitor_report.txt` — Optional output; NIP-66 relay liveness report (if `--check-monitors` used).

## Install & Run

- Requires Go 1.22+

```
cd go-router
go build -o nostr-go-router ./...
```

Collect your relay list, follows, and their relay lists:
```
./nostr-go-router collect \
  --pubkey <your-64-hex-pubkey> \
  --data-dir ./relay_data \
  --relays "wss://relay.damus.io,wss://nos.lol,wss://nostr.wine" \
  --batch-size 50 \
  --parallel 4
```

This will:
1. Fetch your relay list (kind 10002) and save to `user_relay_list.txt`
2. Fetch your follow list (kind 3) and save to `follows_list.txt`
3. Fetch relay lists (kind 10002) for all your follows and save to `all_relay_lists.jsonl`

**Performance notes:**
- Opens **one connection per relay** and reuses it for all batches (efficient!)
- Uses EOSE (End Of Stored Events) detection to exit as soon as relays finish sending data
- Default: 4 parallel relays, 50 authors per batch, 12 second timeout per batch
- Increase `--parallel` to query more relays simultaneously (e.g., `--parallel 6` for all default relays)
- Each batch typically completes in 1-3 seconds via EOSE; timeout is a safety fallback

Analyze (reads `relay_data/all_relay_lists.jsonl` and `relay_data/follows_list.txt`):
```
./nostr-go-router analyze \
  --data-dir ./relay_data
```

Optionally check relay liveness using NIP-66 monitors:
```
./nostr-go-router analyze \
  --data-dir ./relay_data \
  --check-monitors \
  --monitor-relays "wss://relay.nostr.band,wss://history.nostr.watch" \
  --monitor-timeout 10
```

This queries NIP-66 relay monitors for kind 30166 events and generates a report showing:
- **Online/Offline status** for each relay
- **RTT (Round Trip Time)** metrics (open, read, write)
- **Monitor count** - how many monitors reported data
- **Last checked** timestamp

Output files:
- `relay_monitor_report.txt` - Full monitoring report
- `pubkey_relays_map_online.txt` - Filtered map with only online relays

Generate router config (optionally using only online relays):
```
./nostr-go-router gen-router \
  --data-dir ./relay_data \
  --output ./strfry-router.config \
  --authors-per-stream 300 \
  --stream-prefix follows \
  --include-unassigned \
  --replicas 1
```

To use only online relays (requires running analyze with `--check-monitors` first):
```
./nostr-go-router gen-router \
  --data-dir ./relay_data \
  --output ./strfry-router.config \
  --online-only \
  --authors-per-stream 300 \
  --replicas 1
```

Optional filters:
- `--kinds-json '[0,1,3,6,7]'` to limit down-stream REQs.

Include notification streams in router config:
```
./nostr-go-router gen-router \
  --data-dir ./relay_data \
  --output ./strfry-router.config \
  --include-notifs
```

This reads `user_pubkey.txt` and `user_relay_list.txt` (created by `collect` command) and adds streams for each of YOUR relays:
- **Outbox streams**: Your own posts using `{"authors": ["<your-pubkey>"]}` filter
- **Inbox streams**: Notifications mentioning you using `{"#p": ["<your-pubkey>"]}` filter

Note: You must run `collect` with `--pubkey` first to populate these files.

## Features

- **Intelligent relay selection**: Uses greedy set cover algorithm to minimize relay connections while maximizing author coverage
- **Progress tracking**: Real-time progress output during collection with event counts and batch completion
- **Notification streams**: Add inbox/outbox streams to router config for syncing your personal posts and mentions
- **NIP-66 relay monitoring**: Check relay liveness and performance using community monitors
- **Flexible configuration**: Control batch sizes, parallelism, timeouts, and replica counts
- **URL validation**: Automatically filters out invalid relay URLs (with query parameters, etc.)
