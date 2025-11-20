package utils

import (
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/urfave/cli/v2"
)

const (
	FlashblockCategory = "FLASHBLOCKS"
)

var (
	FlashblockAddress = &cli.StringFlag{
		Name:     "flashblock.address",
		Usage:    "Address where to reach the Flashblock provider WebSocket endpoint, e.g. ws://localhost:1114",
		Category: FlashblockCategory,
		Value:    "ws://localhost:1114",
	}
)

var (
	FlashblockFlags = []cli.Flag{
		FlashblockAddress,
	}
)

func fillFlashblockConfigFromFlags(ctx *cli.Context, cfg *ethconfig.Config) {
	flashblockAddress := ctx.String(FlashblockAddress.Name)
	if flashblockAddress != "" {
		cfg.FlashblocksWSURL = flashblockAddress
		cfg.FlashblocksEnabled = true
	}
}
