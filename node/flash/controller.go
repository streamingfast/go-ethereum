package flash

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// FlashblockState represents the accumulated state of a flashblock
type FlashblockState struct {
	// From Static (set only once when index is 0)
	PayloadID             hexutil.Bytes
	ParentBeaconBlockRoot *common.Hash
	ParentHash            common.Hash
	FeeRecipient          common.Address
	PrevRandao            common.Hash
	BlockNumber           uint64
	GasLimit              uint64
	Timestamp             uint64
	ExtraData             []byte
	BaseFeePerGas         *hexutil.Big

	// From Delta (accumulated across all indices)
	StateRoot       common.Hash
	ReceiptsRoot    common.Hash
	LogsBloom       []byte
	BlockHash       common.Hash
	GasUsed         uint64
	Transactions    []hexutil.Bytes
	Withdrawals     []*types.Withdrawal
	WithdrawalsRoot *common.Hash

	// From Metadata (accumulated across all indices)
	AccountBalances map[string]*hexutil.Big // address -> balance
	Receipts        map[string]*Receipt     // tx hash -> receipt

	// Tracking
	CurrentIndex uint64
	MessageCount uint64
}

// NewFlashblockState creates a new empty flashblock state
func NewFlashblockState() *FlashblockState {
	return &FlashblockState{
		Transactions:    make([]hexutil.Bytes, 0),
		Withdrawals:     make([]*types.Withdrawal, 0),
		AccountBalances: make(map[string]*hexutil.Big),
		Receipts:        make(map[string]*Receipt),
	}
}

// Controller manages flashblock state by consuming messages from a message provider
type Controller struct {
	chain    ChainInterface
	provider FlashblockMessageProvider
	logger   log.Logger

	mu    sync.RWMutex
	state *FlashblockState

	// Lifecycle management
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// ChainInterface defines the minimal interface required from the chain
// to implement the flash block functionality. This is usually provided by
// the [core.Blockchain] implementation directly
type ChainInterface interface {
	GetBlock(hash common.Hash, number uint64) *types.Block
	StateAt(stateRoot common.Hash) (*state.StateDB, error)
	HeaderChain() *core.HeaderChain
	Config() *params.ChainConfig
}

// NewController creates a new flashblock controller
func NewController(chain ChainInterface, provider FlashblockMessageProvider, logger log.Logger) *Controller {
	return &Controller{
		chain:    chain,
		provider: provider,
		logger:   logger,
		state:    NewFlashblockState(),
		done:     make(chan struct{}),
	}
}

// Start starts the controller in a background goroutine
func (c *Controller) Start() error {
	if c.ctx != nil {
		return fmt.Errorf("controller already started")
	}

	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.logger.Info("Starting flashblock controller")

	go c.run()

	return nil
}

// Stop stops the controller and waits for it to finish
func (c *Controller) Stop() error {
	var err error
	c.stopOnce.Do(func() {
		c.logger.Info("Stopping flashblock controller")

		if c.cancel != nil {
			c.cancel()

			// Wait for the goroutine to finish
			<-c.done
		}

		// Close the provider connection
		if closeErr := c.provider.Close(); closeErr != nil {
			c.logger.Error("Error closing provider", "error", closeErr)
			err = closeErr
		}

		c.logger.Info("Flashblock controller stopped")
	})
	return err
}

// run is the internal main loop, reading messages from the provider
// and updating state accordingly. Runs until context is cancelled.
func (c *Controller) run() {
	defer close(c.done)

	c.logger.Info("Flashblock controller loop started")

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("Flashblock controller loop stopping")
			return
		default:
			msg, err := c.provider.ReadMessage()
			if err != nil {
				c.logger.Error("Error reading flashblock message", "error", err)
				return
			}

			if err := c.processMessage(msg); err != nil {
				c.logger.Error("Error processing flashblock message", "error", err, "index", msg.Index)
				continue
			}
		}
	}
}

// processMessage processes a flashblock message and updates the state
func (c *Controller) processMessage(msg *FlashblocksPayloadV1) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If this is a base message (index 0), reset the state
	if msg.Index == 0 {
		c.logger.Info("Received base flashblock, resetting state", "payload_id", msg.PayloadID.String())
		c.resetState(msg)
		return nil
	}

	// Verify this is the expected next index
	if msg.Index != c.state.CurrentIndex+1 {
		return fmt.Errorf("received unexpected index %d, expected %d", msg.Index, c.state.CurrentIndex+1)
	}

	// Verify payload ID matches
	if len(c.state.PayloadID) > 0 && msg.PayloadID.String() != c.state.PayloadID.String() {
		c.logger.Warn("Payload ID mismatch, may indicate new block sequence",
			"current", c.state.PayloadID.String(),
			"received", msg.PayloadID.String())
	}

	c.logger.Debug("Accumulating flashblock delta", "index", msg.Index, "payload_id", msg.PayloadID.String())
	c.accumulateDelta(msg)

	// Ready for execution - execute and validate the block
	if err := c.executeAndValidateBlock(); err != nil {
		c.logger.Error("Failed to execute and validate block", "error", err, "index", msg.Index)
		return err
	}

	return nil
}

// resetState resets the state with a base message (index 0)
func (c *Controller) resetState(msg *FlashblocksPayloadV1) {
	// Create new state
	c.state = NewFlashblockState()
	c.state.PayloadID = msg.PayloadID
	c.state.CurrentIndex = 0
	c.state.MessageCount = 1

	// Set base properties
	if msg.Static != nil {
		c.state.ParentBeaconBlockRoot = msg.Static.ParentBeaconBlockRoot
		c.state.ParentHash = msg.Static.ParentHash
		c.state.FeeRecipient = msg.Static.FeeRecipient
		c.state.PrevRandao = msg.Static.PrevRandao
		c.state.BlockNumber = uint64(msg.Static.BlockNumber)
		c.state.GasLimit = uint64(msg.Static.GasLimit)
		c.state.Timestamp = uint64(msg.Static.Timestamp)
		c.state.ExtraData = []byte(msg.Static.ExtraData)
		c.state.BaseFeePerGas = &msg.Static.BaseFeePerGas

		c.logger.Info("Initialized base state",
			"block_number", c.state.BlockNumber,
			"parent_hash", c.state.ParentHash.Hex(),
		)
	}

	// Apply the diff from the base message
	c.applyDiff(&msg.Diff)

	// Apply metadata if present
	c.applyMetadata(&msg.Metadata)
}

// accumulateDelta accumulates a delta message onto the current state
func (c *Controller) accumulateDelta(msg *FlashblocksPayloadV1) {
	c.state.CurrentIndex = msg.Index
	c.state.MessageCount++

	// Apply the diff
	c.applyDiff(&msg.Diff)

	// Apply metadata if present
	c.applyMetadata(&msg.Metadata)

	c.logger.Info("Accumulated delta",
		"index", c.state.CurrentIndex,
		"total_txs", len(c.state.Transactions),
		"gas_used", c.state.GasUsed,
		"block_hash", c.state.BlockHash.Hex(),
	)
}

// applyDiff applies diff information to the current state
func (c *Controller) applyDiff(diff *ExecutionPayloadFlashblockDeltaV1) {
	// Update the latest state values
	c.state.StateRoot = diff.StateRoot
	c.state.ReceiptsRoot = diff.ReceiptsRoot
	c.state.LogsBloom = []byte(diff.LogsBloom)
	c.state.BlockHash = diff.BlockHash
	c.state.GasUsed = uint64(diff.GasUsed)
	c.state.WithdrawalsRoot = diff.WithdrawalsRoot

	// Append new transactions
	if len(diff.Transactions) > 0 {
		c.state.Transactions = append(c.state.Transactions, diff.Transactions...)
		c.logger.Debug("Added transactions",
			"count", len(diff.Transactions),
			"total", len(c.state.Transactions),
		)
	}

	// Append withdrawals if present
	if len(diff.Withdrawals) > 0 {
		for _, w := range diff.Withdrawals {
			c.state.Withdrawals = append(c.state.Withdrawals, w)
		}
		c.logger.Debug("Added withdrawals",
			"count", len(diff.Withdrawals),
			"total", len(c.state.Withdrawals),
		)
	}
}

// applyMetadata applies metadata information to the current state
func (c *Controller) applyMetadata(metadata *FlashblocksMetadata) {
	// Update account balances
	if len(metadata.NewAccountBalances) > 0 {
		for addr, balance := range metadata.NewAccountBalances {
			c.state.AccountBalances[addr] = &balance
		}
		c.logger.Debug("Updated account balances",
			"count", len(metadata.NewAccountBalances),
			"total", len(c.state.AccountBalances),
		)
	}

	// Add receipts
	if len(metadata.Receipts) > 0 {
		maps.Copy(c.state.Receipts, metadata.Receipts)

		c.logger.Debug("Added receipts",
			"count", len(metadata.Receipts),
			"total", len(c.state.Receipts),
		)
	}
}

// executeAndValidateBlock executes and validates the current flashblock state
// Assumes the lock is already held by the caller
func (c *Controller) executeAndValidateBlock() error {
	// Check if parent block and state exist
	parentBlock := c.chain.GetBlock(c.state.ParentHash, c.state.BlockNumber-1)
	if parentBlock == nil {
		return fmt.Errorf("parent block not found: hash=%s, number=%d", c.state.ParentHash.Hex(), c.state.BlockNumber-1)
	}

	parentStateDB, err := c.chain.StateAt(parentBlock.Root())
	if err != nil {
		if errors.Is(err, errors.New("not found")) {
			return fmt.Errorf("parent state not found: hash=%s, number=%d", c.state.ParentHash.Hex(), c.state.BlockNumber-1)
		}

		return fmt.Errorf("failed to get parent state: hash=%s, number=%d",
			c.state.ParentHash.Hex(), c.state.BlockNumber-1)
	}

	config := c.chain.Config()

	// Deal with hard forks, only Isthmus and after (hence why versionnedHash is straight the empty slice)
	versionnedHash := []common.Hash{}
	requests := [][]byte(nil)
	if config.IsIsthmus(c.state.Timestamp) {
		requests = [][]byte{}
	}

	block, err := engine.ExecutableDataToBlock(c.stateToExecutableData(), versionnedHash, c.state.ParentBeaconBlockRoot, requests, c.chain.Config())
	if err != nil {
		return fmt.Errorf("failed to convert executable data to block: %w", err)
	}

	c.logger.Info("Converted flashblock to block",
		"block_number", block.NumberU64(),
		"block_hash", block.Hash().Hex(),
		"tx_count", len(block.Transactions()),
	)

	tracer := tracers.LiveDirectory("firehose")

	processor := core.NewStateProcessor(c.chain.Config(), c.chain.HeaderChain())
	result, err := processor.Process(block, parentStateDB, vm.Config{})
	if err != nil {
		return fmt.Errorf("failed to process block: %w", err)
	}

	c.logger.Info("Processed block",
		"gas_used", result.GasUsed,
		"receipts", len(result.Receipts),
	)

	// Create BlockValidator and validate the processed state
	validator := core.NewBlockValidator(c.chain.Config(), nil)
	if err := validator.ValidateState(block, parentStateDB, result, false); err != nil {
		return fmt.Errorf("failed to validate block state: %w", err)
	}

	c.logger.Info("Validated block successfully",
		"block_number", block.NumberU64(),
		"block_hash", block.Hash().Hex(),
	)

	return nil
}

// stateToExecutableData converts the current flashblock state to ExecutableData
// Assumes the lock is already held by the caller
func (c *Controller) stateToExecutableData() engine.ExecutableData {
	// Convert transactions from hexutil.Bytes to []byte
	transactions := make([][]byte, len(c.state.Transactions))
	for i, tx := range c.state.Transactions {
		transactions[i] = []byte(tx)
	}

	return engine.ExecutableData{
		ParentHash:      c.state.ParentHash,
		FeeRecipient:    c.state.FeeRecipient,
		StateRoot:       c.state.StateRoot,
		ReceiptsRoot:    c.state.ReceiptsRoot,
		LogsBloom:       c.state.LogsBloom,
		Random:          c.state.PrevRandao,
		Number:          c.state.BlockNumber,
		GasLimit:        c.state.GasLimit,
		GasUsed:         c.state.GasUsed,
		Timestamp:       c.state.Timestamp,
		ExtraData:       c.state.ExtraData,
		BaseFeePerGas:   c.state.BaseFeePerGas.ToInt(),
		BlockHash:       c.state.BlockHash,
		Transactions:    transactions,
		Withdrawals:     c.state.Withdrawals,
		WithdrawalsRoot: c.state.WithdrawalsRoot,
	}
}

// PrintState prints the current state to the logger
func (c *Controller) PrintState() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	c.logger.Info("Current Flashblock State",
		"payload_id", c.state.PayloadID.String(),
		"block_number", c.state.BlockNumber,
		"index", c.state.CurrentIndex,
		"message_count", c.state.MessageCount,
	)

	c.logger.Info("  Block Properties",
		"parent_hash", c.state.ParentHash.Hex(),
		"fee_recipient", c.state.FeeRecipient.Hex(),
		"gas_limit", c.state.GasLimit,
		"timestamp", c.state.Timestamp,
	)

	if c.state.BaseFeePerGas != nil {
		c.logger.Info("  Gas",
			"base_fee_per_gas", c.state.BaseFeePerGas.ToInt(),
			"gas_used", c.state.GasUsed,
		)
	}

	c.logger.Info("  State",
		"state_root", c.state.StateRoot.Hex(),
		"block_hash", c.state.BlockHash.Hex(),
		"tx_count", len(c.state.Transactions),
		"withdrawal_count", len(c.state.Withdrawals),
		"account_balance_count", len(c.state.AccountBalances),
		"receipt_count", len(c.state.Receipts),
	)
}
