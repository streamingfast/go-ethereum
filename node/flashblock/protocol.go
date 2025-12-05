package flashblock

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// ProtocolMessageProvider is an interface for reading flashblock messages
type ProtocolMessageProvider interface {
	ReadMessage() (*FlashblocksPayloadV1, error)
	Close() error
}

// FlashblocksPayloadV1 represents the complete flashblock message received over WebSocket
type FlashblocksPayloadV1 struct {
	// Version is actually exactly 4 bytes according to the spec, refactor to have the
	// correct type exact type
	Version hexutil.Bytes `json:"version"`
	// PayloadID is actually 8 bytes according to the spec, refactor to have the
	// correct type
	PayloadID       hexutil.Bytes                     `json:"payload_id"`
	ParentFlashHash *common.Hash                      `json:"parent_flash_hash,omitempty"`
	Index           uint64                            `json:"index"`
	Static          *ExecutionPayloadStaticV1         `json:"base,omitempty"`
	Diff            ExecutionPayloadFlashblockDeltaV1 `json:"diff"`

	// Not parsed for now, so ignore such fields
	// Metadata        FlashblocksMetadata               `json:"metadata"`
}

// ExecutionPayloadStaticV1 contains the initial block properties (only present when index is 0)
type ExecutionPayloadStaticV1 struct {
	ParentBeaconBlockRoot *common.Hash   `json:"parent_beacon_block_root,omitempty"`
	ParentHash            common.Hash    `json:"parent_hash"`
	FeeRecipient          common.Address `json:"fee_recipient"`
	PrevRandao            common.Hash    `json:"prev_randao"`
	BlockNumber           hexutil.Uint64 `json:"block_number"`
	GasLimit              hexutil.Uint64 `json:"gas_limit"`
	Timestamp             hexutil.Uint64 `json:"timestamp"`
	ExtraData             hexutil.Bytes  `json:"extra_data"`
	BaseFeePerGas         hexutil.Big    `json:"base_fee_per_gas"`
}

// ExecutionPayloadFlashblockDeltaV1 contains the incremental changes for each flashblock
type ExecutionPayloadFlashblockDeltaV1 struct {
	StateRoot       common.Hash         `json:"state_root"`
	ReceiptsRoot    common.Hash         `json:"receipts_root"`
	LogsBloom       hexutil.Bytes       `json:"logs_bloom"`
	GasUsed         hexutil.Uint64      `json:"gas_used"`
	BlobGasUsed     hexutil.Uint64      `json:"blob_gas_used"`
	BlockHash       common.Hash         `json:"block_hash"`
	Transactions    []hexutil.Bytes     `json:"transactions"`
	Withdrawals     []*types.Withdrawal `json:"withdrawals,omitempty"`
	WithdrawalsRoot *common.Hash        `json:"withdrawals_root,omitempty"`
}

// The protocol spec https://specs.optimism.io/protocol/flashblocks.html#metadata and the
// Reth implementation https://github.com/paradigmxyz/reth/blob/3afe69a5738459a7cb5f46c598c7f541a1510f32/crates/optimism/flashblocks/src/payload.rs#L67-L76
// differs in the structure defined, quiet possibly due to evolution of the spec over time.
//
// We put the Reth version here but it's untested, for now we just keep metadata
// since it's not required for our use case.
// FlashblocksMetadata contains additional information for flashblocks
// type FlashblocksMetadata struct {
// 	BlockNumber        uint64                    `json:"block_number"`
// 	NewAccountBalances map[string]hexutil.Big    `json:"new_account_balances,omitempty"`
// 	Receipts           map[string]*types.Receipt `json:"receipts,omitempty"`
// }
