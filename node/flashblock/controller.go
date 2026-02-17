package flashblock

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

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

type nextMessage struct {
	blockNum   uint64
	parentHash *common.Hash
}

// Controller manages flashblock state by consuming messages from a message provider
type Controller struct {
	chain    ChainInterface
	provider ProtocolMessageProvider
	logger   log.Logger

	state                    *Sequence
	usedPreviousStateDB      *state.StateDB
	previousFinalizedStateDB *state.StateDB
	PreviousBlockHash        *common.Hash

	// Lifecycle management
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	stopOnce   sync.Once
	msgChannel *PeekChan[*FlashblocksPayloadV1]

	tracer *tracers.Firehose
}

type PeekChan[T any] struct {
	C      chan T
	buf    T
	hasBuf bool
}

func NewPeekChan[T any](ch chan T) *PeekChan[T] {
	return &PeekChan[T]{C: ch}
}

func (p *PeekChan[T]) Peek() (T, bool) {
	var zero T
	if !p.hasBuf {
		if len(p.C) == 0 {
			return zero, false
		}
		v, ok := <-p.C
		if !ok {
			return zero, false
		}
		p.buf = v
		p.hasBuf = true
	}
	return p.buf, true
}

func (p *PeekChan[T]) Next(ctx context.Context) (T, bool) {
	if ctx.Err() != nil {
		var zero T
		return zero, false
	}
	if p.hasBuf {
		p.hasBuf = false
		return p.buf, true
	}
	select {
	case <-ctx.Done():
		var zero T
		return zero, false
	case v, ok := <-p.C:
		return v, ok
	}
}

// NewController creates a new flashblock controller
func NewController(chain ChainInterface, provider ProtocolMessageProvider, logger log.Logger) *Controller {
	tracer := tracers.NewFirehose(&tracers.FirehoseConfig{
		// Not clear what op-geth uses, need to validate that and ensure we have the same semantics
		// ApplyBackwardCompatibility: *bool,
	})

	tracer.OnBlockchainInit(chain.Config())

	return &Controller{
		chain:      chain,
		provider:   provider,
		logger:     logger,
		state:      NewFlashblockState(),
		done:       make(chan struct{}),
		msgChannel: NewPeekChan(make(chan *FlashblocksPayloadV1, 10)),
		tracer:     tracer,
	}
}

// Start starts the controller in a background goroutine
func (c *Controller) Start() error {
	if c.ctx != nil {
		return fmt.Errorf("controller already started")
	}

	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.logger.Info("Starting flashblock controller")

	go c.readLoop()
	go c.processLoop()

	return nil
}

// SendNotification sends a notification message to the controller for logging purposes
// This is used to notify the controller about blocks from external sources (e.g., NewPayloadV4, V3...)
func (c *Controller) SendNotification(blockNumber uint64, blockHash common.Hash) {
	if c.ctx == nil || c.ctx.Err() != nil {
		// Controller not started or already stopped
		return
	}

	// Create a special notification message with version 0xdeadbeef
	// Hide blockNumber in Index and blockHash in ParentFlashHash
	notification := &FlashblocksPayloadV1{
		Version:         hexutil.Bytes{0xde, 0xad, 0xbe, 0xef},
		Index:           blockNumber,
		ParentFlashHash: &blockHash,
	}

	// Try to send the notification without blocking
	select {
	case c.msgChannel.C <- notification:
		// Message sent successfully
	case <-c.ctx.Done():
		// Controller stopped
	default:
		// Channel full, skip this notification
		c.logger.Warn("Failed to send flashblock notification, channel full", "blockNumber", blockNumber)
	}
}

// isNotificationMessage checks if a message is a special notification message (version 0xdeadbeef)
// and returns the block number, block hash, and whether it's a notification
func isNotificationMessage(msg *FlashblocksPayloadV1) (blockNumber uint64, blockHash common.Hash, ok bool) {
	// Check if this is a special notification message (version 0xdeadbeef)
	if len(msg.Version) == 4 && msg.Version[0] == 0xde && msg.Version[1] == 0xad &&
		msg.Version[2] == 0xbe && msg.Version[3] == 0xef {
		// blockNumber is in Index, blockHash is in ParentFlashHash
		if msg.ParentFlashHash != nil {
			return msg.Index, *msg.ParentFlashHash, true
		}
		return msg.Index, common.Hash{}, true
	}
	return 0, common.Hash{}, false
}

// Stop stops the controller and waits for it to finish
func (c *Controller) Stop() error {
	var err error
	c.stopOnce.Do(func() {
		c.logger.Info("Stopping flashblock controller")

		if c.cancel != nil {
			c.cancel()

			// Close the message channel
			close(c.msgChannel.C)

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

// readLoop reads messages from the provider and sends them to the message channel
func (c *Controller) readLoop() {
	c.logger.Info("Flashblock read loop started")

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("Flashblock read loop stopping")
			return
		default:
			msg, err := c.provider.ReadMessage()
			if err != nil {
				c.logger.Error("Error reading flashblock message", "error", err)
				continue
			}

			var blkNum uint64
			if msg.Static != nil {
				blkNum = uint64(msg.Static.BlockNumber)
			}

			c.logger.Info("Received flashblock message", "index", msg.Index, "blockNumber", blkNum)

			select {
			case c.msgChannel.C <- msg:
			case <-c.ctx.Done():
				c.logger.Info("Flashblock read loop stopping")
				return
			}
		}
	}
}

// processLoop processes messages from the message channel
func (c *Controller) processLoop() {
	defer close(c.done)

	c.logger.Info("Flashblock process loop started")

	for {
		msg, ok := c.msgChannel.Next(c.ctx)
		if !ok {
			c.logger.Info("Message channel closed, process loop stopping")
			return
		}

		if err := c.processMessage(msg); err != nil {
			c.logger.Error("Error processing flashblock message", "error", err, "index", msg.Index)
		}
	}
}

// processMessage processes a flashblock message and updates the state
func (c *Controller) processMessage(msg *FlashblocksPayloadV1) error {

	// Check if this is a special notification message
	if blockNumber, blockHash, ok := isNotificationMessage(msg); ok {
		if c.state != nil && !c.state.Skipping && c.state.ExecutableData.Number == blockNumber {
			c.logger.Info("Received block notification from NewPayload",
				"blockNumber", blockNumber,
				"blockHash", blockHash.Hex(),
				"executing", !c.state.FinalPartSent,
			)

			if !c.state.FinalPartSent {
				if c.state.LastSentIndex == c.state.CurrentIndex {
					c.state.CurrentIndex++
				}
				if err := c.executeAndValidateBlock(true, &blockHash); err != nil {
					c.logger.Error("Failed to execute and validate block", "error", err, "index", msg.Index)
					c.state.Skipping = true // don't continue if flash block failed
					return err
				}
				c.state.FinalPartSent = true // do not re-send this when we get the msg.Index=0
			}
		}
		return nil
	}

	// If this is a base message (index 0), reset the state
	if msg.Index == 0 {
		if c.state != nil && !c.state.Skipping && msg.Static != nil {

			// we may have already sent it with the lookahead feature
			if !c.state.FinalPartSent {
				// execute "last partial" for previous block:
				//  - increment index if we already sent this one
				if c.state.LastSentIndex == c.state.CurrentIndex {
					c.state.CurrentIndex++
				}
				//  - proper execution
				if err := c.executeAndValidateBlock(true, &msg.Static.ParentHash); err != nil {
					c.logger.Error("Failed to execute and validate block", "error", err, "index", msg.Index)
					c.state.Skipping = true // don't continue if flash block failed
					return err
				}
			}

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

	var expectedBlockHash *common.Hash
	if nextMsg, ok := c.msgChannel.Peek(); ok {

		for {
			if blockNumber, blockHash, ok := isNotificationMessage(nextMsg); ok {
				c.logger.Info("Received block notification from NewPayload",
					"blockNumber", blockNumber,
					"blockHash", blockHash.Hex())
				_, _ = c.msgChannel.Next(c.ctx) // discard this notification
				// Peek again to get the next message
				nextMsg, ok = c.msgChannel.Peek()
				if !ok {
					// No more messages, break out
					break
				}
			} else {
				// Not a notification message, break out
				break
			}
		}
		if nextMsg.Static != nil {

			if uint64(nextMsg.Static.BlockNumber) == c.state.ExecutableData.Number {
				c.logger.Debug("skipping execution because next message is waiting with same block number", "index", msg.Index, "payload_id", msg.PayloadID.String())
				return nil
			}
			if uint64(nextMsg.Static.BlockNumber) == c.state.ExecutableData.Number+1 {
				// next flaskblock message can tell us if we are the canonical last flashblock: we will finalize the block and see if we match its expected parent hash
				expectedBlockHash = &nextMsg.Static.ParentHash
			}
		}
	}

	isFinalBlock := expectedBlockHash != nil

	// Ready for execution - execute and validate the block only if index is allowed
	if err := c.executeAndValidateBlock(isFinalBlock, expectedBlockHash); err != nil {
		c.logger.Error("Failed to execute and validate block", "error", err, "index", msg.Index)
		c.state.Skipping = true // don't continue if flash block failed
		return err
	}
	c.state.LastSentIndex = c.state.CurrentIndex
	c.state.FinalPartSent = isFinalBlock

	return nil
}

// resetState resets the state with a base message (index 0)
func (c *Controller) resetState(msg *FlashblocksPayloadV1) {
	// Create new state
	c.state = NewFlashblockState()
	c.state.PayloadID = msg.PayloadID
	c.state.CurrentIndex = 0
	c.state.LastSentIndex = 0
	c.state.MessageCount = 1
	c.state.FinalPartSent = false

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
func (c *Controller) executeAndValidateBlock(isLastFlashBlock bool, expectedBlockHash *common.Hash) (err error) {
	bh := c.state.ExecutableData.BlockHash
	if expectedBlockHash != nil {
		bh = *expectedBlockHash
	}
	stats := &flashblockStats{
		blockHash:   bh,
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
		"is_last_flash_block", isLastFlashBlock,
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
		}, isLastFlashBlock)
		stats.processDuration = time.Since(startProcess)
		if err != nil {
			return fmt.Errorf("process block: %w", err)
		}

		if isLastFlashBlock {
			c.PreviousBlockHash = newHash
			c.previousFinalizedStateDB = finalizedStateDB

			c.tracer.SetFinalFlashBlock(*newHash, *newStateRoot)
		}

		startValidate := time.Now()
		err = c.state.Processor.ValidateState(block, result)
		stats.validateDuration = time.Since(startValidate)
		if err != nil {
			log.Error("Block state validation failed", "error", err)
		}

		if isLastFlashBlock {
			if newHash.Cmp(*expectedBlockHash) != 0 {
				return fmt.Errorf("expected root hash %s, got %s", expectedBlockHash.String(), newHash.String())
			}
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
