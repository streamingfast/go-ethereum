package flashblock

import (
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// Sequence represents the accumulated state of a flashblock
type Sequence struct {
	// Skipping indicates whether the flashblock is being skipped due to being too far behind
	Skipping bool
	// ExecutableData contains the block execution data that gets updated over time
	ExecutableData engine.ExecutableData

	// StateProcessor is stateful in regards to the sequence of partial blocks,
	// it is nil initially until the execution can be performed.
	Processor *StateProcessor

	// Flashblock-specific fields
	PayloadID             hexutil.Bytes
	ParentBeaconBlockRoot *common.Hash

	FinalPartSent bool
	LastSentIndex uint64
	CurrentIndex  uint64
	MessageCount  uint64
}

// NewFlashblockState creates a new empty flashblock state
func NewFlashblockState() *Sequence {
	return &Sequence{
		ExecutableData: engine.ExecutableData{
			Transactions:  make([][]byte, 0),
			Withdrawals:   make([]*types.Withdrawal, 0),
			ExcessBlobGas: new(uint64), // mimic behavior of full block
		},
	}
}
