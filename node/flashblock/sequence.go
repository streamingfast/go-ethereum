package flashblock

import (
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// Sequence represents the accumulated state of a flashblock
type Sequence struct {
	// ExecutableData contains the block execution data that gets updated over time
	ExecutableData engine.ExecutableData

	// Flashblock-specific fields
	PayloadID             hexutil.Bytes
	ParentBeaconBlockRoot *common.Hash

	CurrentIndex uint64
	MessageCount uint64
}

// NewFlashblockState creates a new empty flashblock state
func NewFlashblockState() *Sequence {
	return &Sequence{
		ExecutableData: engine.ExecutableData{
			Transactions: make([][]byte, 0),
			Withdrawals:  make([]*types.Withdrawal, 0),
		},
	}
}
