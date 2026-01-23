package flashblock

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

var flashblocksOnlyIdx map[uint64]bool

func init() {
	flashblocksOnlyIdx = make(map[uint64]bool)

	envVal := os.Getenv("FLASHBLOCKS_ONLY_IDX")
	if envVal == "" {
		return
	}

	indices := strings.Split(envVal, ",")
	for _, indexStr := range indices {
		indexStr = strings.TrimSpace(indexStr)
		if index, err := strconv.ParseUint(indexStr, 10, 64); err == nil {
			flashblocksOnlyIdx[index] = true
		}
	}
}

// ChainInterface defines the minimal interface required from the chain
// to implement the flash block functionality. This is usually provided by
// the [core.Blockchain] implementation directly
type ChainInterface interface {
	GetBlock(hash common.Hash, number uint64) *types.Block
	CurrentFinalBlock() *types.Header
	StateAt(stateRoot common.Hash) (*state.StateDB, error)
	HeaderChain() *core.HeaderChain
	Config() *params.ChainConfig
}

// Controller manages flashblock state by consuming messages from a message provider
type Controller struct {
	chain    ChainInterface
	provider ProtocolMessageProvider
	logger   log.Logger

	mu                       sync.RWMutex
	state                    *Sequence
	usedPreviousStateDB      *state.StateDB
	previousFinalizedStateDB *state.StateDB
	PreviousBlockHash        *common.Hash

	// Lifecycle management
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once

	tracer *tracers.Firehose
}

// NewController creates a new flashblock controller
func NewController(chain ChainInterface, provider ProtocolMessageProvider, logger log.Logger) *Controller {
	tracer := tracers.NewFirehose(&tracers.FirehoseConfig{
		// Not clear what op-geth uses, need to validate that and ensure we have the same semantics
		// ApplyBackwardCompatibility: *bool,
	})

	tracer.OnBlockchainInit(chain.Config())

	return &Controller{
		chain:    chain,
		provider: provider,
		logger:   logger,
		state:    NewFlashblockState(),
		done:     make(chan struct{}),
		tracer:   tracer,
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
				continue
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
		if c.state != nil && !c.state.Skipping && msg.Static != nil {
			c.usedPreviousStateDB = c.getStateDB(msg.Static.ParentHash)
		} else {
			c.usedPreviousStateDB = nil
		}

		c.resetState(msg)
		if delay := time.Since(time.Unix(int64(msg.Static.Timestamp), 0)); delay > 0 {
			c.logger.Info("Skipping flashblock because we are too far behind: %dms", delay.Milliseconds())
			c.state.Skipping = true
		} else {
			c.logger.Info("Received base flashblock, resetting state", "payload_id", msg.PayloadID.String())
		}
		return nil
	}

	if c.state.Skipping {
		return nil
	}

	defer func(start time.Time) {
		duration := time.Since(start)

		logMessage := "Processed flashblock message"
		level := log.LevelInfo

		if duration > 175*time.Millisecond {
			logMessage = logMessage + " (took too long)"
			level = log.LevelWarn
		}

		c.logger.Log(level, logMessage,
			"index", msg.Index,
			"payload_id", msg.PayloadID.String(),
			"duration_ms", duration.Milliseconds(),
		)
	}(time.Now())

	// Verify this is the expected next index
	if msg.Index != c.state.CurrentIndex+1 {
		c.logger.Warn("Did not receive expected index message",
			"expected", c.state.CurrentIndex+1,
			"received", msg.Index)
		c.state.Skipping = true
		return nil
	}

	// Verify payload ID matches
	if len(c.state.PayloadID) > 0 && msg.PayloadID.String() != c.state.PayloadID.String() {
		c.logger.Warn("Payload ID mismatch, may indicate new block sequence",
			"current", c.state.PayloadID.String(),
			"received", msg.PayloadID.String())
		c.state.Skipping = true
		return nil
	}

	// We would need to offload that processing to a separate goroutine to stop blocking the
	// websocket reading loop. We probably need to re-think the overall approach anyway to
	// improve the overall architecture of our flashblock handling.

	c.logger.Debug("Accumulating flashblock delta", "index", msg.Index, "payload_id", msg.PayloadID.String())
	c.accumulateDelta(msg)

	// Ready for execution - execute and validate the block only if index is allowed
	if len(flashblocksOnlyIdx) == 0 || flashblocksOnlyIdx[msg.Index] {
		if err := c.executeAndValidateBlock(); err != nil {
			c.logger.Error("Failed to execute and validate block", "error", err, "index", msg.Index)
			return err
		}
	} else {
		c.logger.Debug("Skipping execution for index not in FLASHBLOCKS_ONLY_IDX", "index", msg.Index)
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
		c.state.ExecutableData.ParentHash = msg.Static.ParentHash
		c.state.ExecutableData.FeeRecipient = msg.Static.FeeRecipient
		c.state.ExecutableData.Random = msg.Static.PrevRandao
		c.state.ExecutableData.Number = uint64(msg.Static.BlockNumber)
		c.state.ExecutableData.GasLimit = uint64(msg.Static.GasLimit)
		c.state.ExecutableData.Timestamp = uint64(msg.Static.Timestamp)
		c.state.ExecutableData.ExtraData = []byte(msg.Static.ExtraData)
		c.state.ExecutableData.BaseFeePerGas = msg.Static.BaseFeePerGas.ToInt()

		c.logger.Info("Initialized base state",
			"block_number", c.state.ExecutableData.Number,
			"parent_hash", c.state.ExecutableData.ParentHash.Hex(),
		)
	}

	// Apply the diff from the base message
	c.applyDiff(&msg.Diff)
}

// accumulateDelta accumulates a delta message onto the current state
func (c *Controller) accumulateDelta(msg *FlashblocksPayloadV1) {
	c.state.CurrentIndex = msg.Index
	c.state.MessageCount++

	// Apply the diff
	c.applyDiff(&msg.Diff)

	c.logger.Info("Accumulated delta",
		"index", c.state.CurrentIndex,
		"total_txs", len(c.state.ExecutableData.Transactions),
		"gas_used", c.state.ExecutableData.GasUsed,
		"block_hash", c.state.ExecutableData.BlockHash.TerminalString(),
	)
}

// applyDiff applies diff information to the current state
func (c *Controller) applyDiff(diff *ExecutionPayloadFlashblockDeltaV1) {
	// Update the latest state values
	c.state.ExecutableData.StateRoot = diff.StateRoot
	c.state.ExecutableData.ReceiptsRoot = diff.ReceiptsRoot
	c.state.ExecutableData.LogsBloom = []byte(diff.LogsBloom)
	c.state.ExecutableData.BlockHash = diff.BlockHash
	c.state.ExecutableData.GasUsed = uint64(diff.GasUsed)
	c.state.ExecutableData.BlobGasUsed = (*uint64)(&diff.BlobGasUsed)
	c.state.ExecutableData.WithdrawalsRoot = diff.WithdrawalsRoot

	// Append new transactions (convert from hexutil.Bytes to []byte)
	if len(diff.Transactions) > 0 {
		for _, tx := range diff.Transactions {
			c.state.ExecutableData.Transactions = append(c.state.ExecutableData.Transactions, []byte(tx))
		}
		c.logger.Debug("Added transactions",
			"count", len(diff.Transactions),
			"total", len(c.state.ExecutableData.Transactions),
		)
	}

	// Append withdrawals if present
	if len(diff.Withdrawals) > 0 {
		c.state.ExecutableData.Withdrawals = append(c.state.ExecutableData.Withdrawals, diff.Withdrawals...)

		c.logger.Debug("Added withdrawals",
			"count", len(diff.Withdrawals),
			"total", len(c.state.ExecutableData.Withdrawals),
		)
	}
}

func (c *Controller) getStateDB(parentHash common.Hash) *state.StateDB {
	if c == nil {
		return nil
	}
	if c.state == nil {
		return nil
	}
	if c.state.Processor == nil {
		return nil
	}
	if c.state.Processor.statedb == nil {
		return nil
	}
	if c.PreviousBlockHash.Cmp(parentHash) != 0 {
		return nil
	}

	c.logger.Info("got statedb from previous block", "block_num", c.state.ExecutableData.Number)
	return c.previousFinalizedStateDB
}

func (c *Controller) getParentStateDB() (*state.StateDB, error) {
	// Check if parent block and state exist
	parentBlock := c.chain.GetBlock(c.state.ExecutableData.ParentHash, c.state.ExecutableData.Number-1)
	if parentBlock == nil {
		return nil, nil
	}

	parentStateDB, err := c.chain.StateAt(parentBlock.Root())
	if err != nil {
		if errors.Is(err, errors.New("not found")) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to get parent state: hash=%s, number=%d",
			c.state.ExecutableData.ParentHash.Hex(), c.state.ExecutableData.Number-1)
	}
	return parentStateDB, nil

}

// executeAndValidateBlock executes and validates the current flashblock state
// Assumes the lock is already held by the caller
func (c *Controller) executeAndValidateBlock() (err error) {
	stats := &flashblockStats{
		blockHash:   c.state.ExecutableData.BlockHash,
		blockNumber: c.state.ExecutableData.Number,
	}

	if c.state.Processor == nil {
		parentStateDB, err := c.getParentStateDB()
		if err != nil {
			return err
		}

		if parentStateDB == nil {
			// we use previous stateDB for the first flash blocks when we are not ready
			if c.usedPreviousStateDB != nil {
				parentStateDB = c.usedPreviousStateDB
				c.logger.Info("Using previous stateDB for first flash block",
					"parent_hash", c.state.ExecutableData.ParentHash.Hex(),
					"parent_number", c.state.ExecutableData.Number-1,
				)
				c.usedPreviousStateDB = nil
			} else {
				c.logger.Info("Parent block not found, skipping execution",
					"parent_hash", c.state.ExecutableData.ParentHash.Hex(),
					"parent_number", c.state.ExecutableData.Number-1,
				)
				return nil
			}
		} else {
			c.logger.Debug("Got stateDB from chain state",
				"parent_hash", c.state.ExecutableData.ParentHash.Hex(),
				"parent_number", c.state.ExecutableData.Number-1,
			)
			c.usedPreviousStateDB = nil
		}

		c.state.Processor = NewStateProcessor(
			c.chain.Config(),
			c.chain.HeaderChain(),
			parentStateDB,
			// TODO: We could cache that once since it's static for the whole sequence
			new(big.Int).SetUint64(c.state.ExecutableData.Number),
			c.state.ExecutableData.Timestamp,
			c.state.ExecutableData.GasLimit,
		)
	}

	chainConfig := c.chain.Config()

	// Deal with hard forks, only Isthmus and after (hence why versionnedHash is straight the empty slice)
	versionnedHash := []common.Hash{}
	requests := [][]byte(nil)
	if chainConfig.IsIsthmus(c.state.ExecutableData.Timestamp) {
		requests = [][]byte{}
	}

	var block *types.Block
	block, err = engine.ExecutableDataToBlock(c.state.ExecutableData, versionnedHash, c.state.ParentBeaconBlockRoot, requests, chainConfig)
	if err != nil {
		return fmt.Errorf("failed to convert executable data to block: %w", err)
	}

	c.logger.Info("Converted flashblock to block",
		"block_number", block.NumberU64(),
		"block_hash", block.Hash().TerminalString(),
		"tx_count", len(block.Transactions()),
	)

	currentIndex := c.state.CurrentIndex
	currentFinalBlock := c.chain.CurrentFinalBlock()
	executor := func() (err error) {
		c.tracer.OnBlockStart(tracing.BlockEvent{
			FlashBlock: &tracing.FlashBlock{
				Block: block,
				Idx:   currentIndex,
			},
			Finalized: currentFinalBlock,
		})
		defer func() {
			stats.err = err
			if r := recover(); r != nil {
				stats.err = errors.Join(err, fmt.Errorf("panic during block execution: %v", r))
				debug.PrintStack()
			}

			c.tracer.OnBlockEnd(stats.err)

			c.reportFlashblockStats(stats)
		}()

		startProcess := time.Now()
		result, newStateRoot, newHash, finalizedStateDB, err := c.state.Processor.Process(block, c.tracer, vm.Config{
			Tracer: tracers.NewTracingHooksFromFirehose(c.tracer),
		})
		c.PreviousBlockHash = newHash
		c.previousFinalizedStateDB = finalizedStateDB
		stats.processDuration = time.Since(startProcess)
		if err != nil {
			return fmt.Errorf("process block: %w", err)
		}

		if newStateRoot != nil {
			c.tracer.SetStateRoot(*newStateRoot)
		}
		if newHash != nil {
			c.tracer.SetHash(*newHash)
		}

		startValidate := time.Now()
		err = c.state.Processor.ValidateState(block, result)
		stats.validateDuration = time.Since(startValidate)
		if err != nil {
			log.Error("Block state validation failed", "error", err)
			c.state.Skipping = true // don't continue if flash block failed
		}

		return err
	}
	executor()

	return nil
}

type flashblockStats struct {
	blockHash        common.Hash
	blockNumber      uint64
	processDuration  time.Duration
	validateDuration time.Duration
	err              error
}

func (c *Controller) reportFlashblockStats(stats *flashblockStats) {
	logMsg := "Executed block"
	logLevel := log.LevelInfo

	if stats.err != nil {
		logMsg = fmt.Sprintf("Failed to execute block: %s", stats.err.Error())
		logLevel = log.LevelError
	}

	c.logger.Log(logLevel, logMsg,
		"number", stats.blockNumber,
		"hash", stats.blockHash.TerminalString(),
		"process_ms", stats.processDuration.Milliseconds(),
		//		"validate_ms", stats.validateDuration.Milliseconds(),
	)
}
