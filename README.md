# nostr-go-router

Go-based tools to analyze kind-10002 relay lists and generate a `strfry router` config.

Subcommands:

- `analyze` — Parse JSONL `10002` events, build READ/WRITE pubkey→relay maps, apply exclude hosts, compute optimal relay set (greedy), and derive outbox relays.
- `gen-router` — Generate a `strfry router` taocpp::config file using per-relay authors and the computed sets.

Data directory (defaults to `relay_data/` next to where you run the command):

- `all_relay_lists.jsonl` — Input JSONL of kind-10002 events (from your existing collection).
- `follows_list.txt` — Input list of your follows (one 64-hex pubkey per line).
- `outbox_exclude.txt` — Optional input list of relays you do NOT want to publish to (one URL per line).
- `pubkey_relays_map_read.txt` — Output; pubkey→relay mapping for read/REQ coverage.
- `pubkey_relays_map_write.txt` — Output; pubkey→relay mapping for outbox/write.
- `optimal_relay_set.txt` — Output; relays chosen by greedy set cover (from READ map, excludes honored).
- `outbox_relays.txt` — Output; relays for uploads derived from WRITE map, excludes honored.

## Install & Run

- Requires Go 1.22+

```
cd go-router
go build -o nostr-go-router ./...
```

Analyze (reads `relay_data/all_relay_lists.jsonl` and `relay_data/follows_list.txt`):
```
./nostr-go-router analyze \
  --data-dir ../relay_data
```

Generate router config:
```
./nostr-go-router gen-router \
  --data-dir ../relay_data \
  --output ../strfry-router.config \
  --authors-per-stream 300 \
  --stream-prefix follows \
  --include-unassigned \
  --create-outbox \
  --outbox-stream-prefix outbox
```

Optional filters:
- `--kinds-json '[0,1,3,6,7]'` to limit down-stream REQs.
- `--outbox-kinds-json '[1,7]'` to restrict uploads.

## Roadmap
- Optional `collect` subcommand using `go-nostr` to replace the bash collector.
