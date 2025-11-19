package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	sub := os.Args[1]
	switch sub {
	case "analyze":
		analyzeCmd(os.Args[2:])
	case "gen-router":
		genRouterCmd(os.Args[2:])
	case "collect":
		collectCmd(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", sub)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("feedbuilder <subcommand> [flags]")
	fmt.Println("\nSubcommands:")
	fmt.Println("  collect     Fetch follows (kind 3) and relay lists (kind 10002) into data dir")
	fmt.Println("  analyze     Parse 10002 JSONL, build maps, apply excludes, compute optimal and outbox sets")
	fmt.Println("  gen-router  Generate strfry router config from analysis outputs")
	fmt.Println("\nUse '<subcommand> -h' for flags.")
}

func commonFlags(fs *flag.FlagSet) (dataDir *string) {
	return fs.String("data-dir", "./relay_data", "path to data directory (inputs/outputs)")
}
