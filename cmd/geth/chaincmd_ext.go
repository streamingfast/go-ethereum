package main

import (
	"github.com/urfave/cli/v2"
)

var (
	importFromFirehoseCommand = &cli.Command{
		Action:    importFromFirehose,
		Name:      "import-from-firehose",
		Usage:     "Import blocks from a Firehose gRPC endpoint directly into the chain database",
		ArgsUsage: "<firehose-endpoint> <chainID> <rpc>",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "batch-size",
				Usage: "Number of blocks to import per batch",
				Value: 10,
			},
			&cli.IntFlag{
				Name:  "start-block",
				Usage: "Start block number (inclusive, default: current block)",
			},
			&cli.Uint64Flag{
				Name:  "end-block",
				Usage: "End block number (inclusive, default: unlimited)",
				Value: 0,
			},
			&cli.IntFlag{
				Name:  "worker-count",
				Usage: "Number of concurrent workers for block conversion (default: 100)",
				Value: 10,
			},
			&cli.IntFlag{
				Name:  "firehose-buffer-size",
				Usage: "Buffer size for Firehose block conversion channels (default: 100)",
				Value: 10,
			},
		},
		Description: `
Connects to a Firehose gRPC endpoint, streams Ethereum blocks, and imports them directly into the Geth chain database.

Required arguments:
  <firehose-endpoint>   The Firehose gRPC endpoint to connect to
  <chainID>            The chain ID to use for block conversion
  <rpc>                The external rpc provider to fill in missing data

The API token for the Firehose endpoint can be provided via the FIREHOSE_API_TOKEN environment variable.
`,
	}
)
