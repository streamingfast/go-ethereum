//go:build integration
// +build integration

package bor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
)

// firehoseHarnessOptions tweaks the Bor chain a firehoseHarness runs. Zero value means a chain
// with sprint of [sprintSize] blocks, Madhugiri disabled, no state-sync events.
type firehoseHarnessOptions struct {
	// MadhugiriBlock is the activation block of Madhugiri, nil keeps it disabled (pre-Madhugiri state-sync).
	MadhugiriBlock *big.Int

	// RioBlock is the activation block of Rio, nil keeps it disabled (span commit system transactions).
	RioBlock *big.Int

	// StateSyncBatches lists the number of state-sync events to serve per sprint-end block, in order, the
	// first value is for the first sprint-end block (block [sprintSize]), and so on. A zero or missing
	// value means no event for that sprint.
	StateSyncBatches []int

	// RealSpanner keeps Bor's own spanner instead of the mock, so span commits execute the real validator
	// set contract (system call to 0x1000) of the genesis and reach the tracer.
	RealSpanner bool

	// PrecompileCache turns the precompile-cache flag on, so a block that fails is executed a second time by
	// the serial retry without shared caches, which reopens the block on the tracer.
	PrecompileCache bool

	// PrintGenesisBlock keeps the genesis block in the Firehose output instead of ignoring it.
	PrintGenesisBlock bool
}

// firehoseHarness drives a real Bor chain (eth.New wiring, Heimdall and spanner mocks) with the production
// Firehose live tracer attached, flushing to an in-memory buffer, through the same
// InsertChain -> StateProcessor.Process -> Bor.Finalize path production uses.
type firehoseHarness struct {
	t       *testing.T
	fh      *tracers.Firehose
	init    *initializeData
	chain   *core.BlockChain
	bor     *bor.Bor
	valSet  []*valset.Validator
	head    *types.Block
	genesis *types.Block
}

// stateSyncEventsPerBatchSpacing is the time between state-sync event batches, matches the time between two
// sprint-end blocks with the chain's configuration (3 blocks of 1s and a sprint-end block of 6s).
const stateSyncEventsPerBatchSpacing = 9

func newFirehoseHarness(t *testing.T, opts firehoseHarnessOptions) *firehoseHarness {
	t.Helper()

	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	h := &firehoseHarness{t: t}

	// Registered per test (unique name), the tracer instance is captured so the test can read the buffer.
	tracerName := "firehose-bor-" + strings.ReplaceAll(t.Name(), "/", "-")
	tracers.LiveDirectory.Register(tracerName, func(_ json.RawMessage) (*tracing.Hooks, error) {
		fh, err := tracers.NewFirehoseFromRawJSON(fmt.Appendf(nil, `{"_private":{"flushToTestBuffer":true,"ignoreGenesisBlock":%t}}`, !opts.PrintGenesisBlock))
		if err != nil {
			return nil, err
		}
		h.fh = fh
		return fh.TracingHooks(), nil
	})

	const stateSyncConfirmationDelay = 128
	updateGenesis := func(gen *core.Genesis) {
		gen.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": stateSyncConfirmationDelay}
		gen.Config.Bor.Sprint = map[string]uint64{"0": sprintSize}
		gen.Config.Bor.MadhugiriBlock = opts.MadhugiriBlock
		gen.Config.Bor.RioBlock = opts.RioBlock
	}

	h.init = buildEthereumInstanceWithConfig(t, rawdb.NewMemoryDatabase(), eth.Config{VMTrace: tracerName, EnablePrecompileCache: opts.PrecompileCache}, updateGenesis)
	require.NotNil(t, h.fh, "firehose tracer must have been instantiated by eth.New")

	h.chain = h.init.ethereum.BlockChain()
	h.bor = h.init.ethereum.Engine().(*bor.Bor)
	t.Cleanup(func() { _ = h.bor.Close() })

	span0 := createMockSpan(addr, h.chain.Config().ChainID.String())
	h.valSet = borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet).Validators
	if !opts.RealSpanner {
		h.bor.SetSpanner(getMockedSpanner(t, h.valSet))
	}

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	heimdall := createMockHeimdall(ctrl, &span0, loadSpanFromFile(t))
	heimdall.EXPECT().GetLatestSpan(gomock.Any()).Return(nil, fmt.Errorf("span not found")).AnyTimes()
	heimdall.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		h.serveStateSyncEvents(opts.StateSyncBatches, int64(h.chain.GetHeaderByNumber(0).Time)-stateSyncConfirmationDelay),
	).AnyTimes()
	h.bor.SetHeimdallClient(heimdall)

	h.genesis = h.init.genesis.ToBlock()
	h.head = h.genesis

	return h
}

// serveStateSyncEvents serves, for each sprint-end block, the events of its batch only: event IDs are
// consecutive across batches and every event of batch N is timestamped strictly before the "to" bound
// Bor computes for the Nth sprint-end block but after the one of the previous sprint-end block.
func (h *firehoseHarness) serveStateSyncEvents(batches []int, baseTime int64) func(context.Context, uint64, int64) ([]*clerk.EventRecordWithTime, error) {
	sample := getSampleEventRecord(h.t)

	var all []*clerk.EventRecordWithTime
	for batch, count := range batches {
		// "to" of sprint-end block N+1 is baseTime + 9*(N+1), events must be below it
		batchEnd := baseTime + stateSyncEventsPerBatchSpacing*int64(batch+1)
		for i := range count {
			id := uint64(len(all) + 1)
			all = append(all, buildStateEvent(sample, id, batchEnd-int64(count-i)))
		}
	}

	return func(_ context.Context, from uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
		var out []*clerk.EventRecordWithTime
		for _, event := range all {
			if event.ID >= from && event.Time.Unix() < to {
				out = append(out, event)
			}
		}
		return out, nil
	}
}

// Advance builds and inserts the next block, using txs (that must be valid for the block) and returns it.
func (h *firehoseHarness) Advance(txs ...*types.Transaction) *types.Block {
	h.t.Helper()

	h.head = buildNextBlock(h.t, h.bor, h.chain, h.head, nil, h.init.genesis.Config.Bor, txs, h.valSet, false, nil, nil)
	insertNewBlock(h.t, h.chain, h.head)
	return h.head
}

// Build builds the next block on top of the head without inserting it, modifyBody can tamper with it.
func (h *firehoseHarness) Build(modifyBody ...modifyBlockFunc) *types.Block {
	h.t.Helper()

	return buildNextBlock(h.t, h.bor, h.chain, h.head, nil, h.init.genesis.Config.Bor, nil, h.valSet, false, nil, modifyBody)
}

// AdvanceTo builds and inserts empty blocks until block number `num` (included).
func (h *firehoseHarness) AdvanceTo(num uint64) *types.Block {
	h.t.Helper()

	for h.head.NumberU64() < num {
		h.Advance()
	}
	return h.head
}

// FirehoseBlockCount returns the number of FIRE BLOCK lines printed so far for block `num`.
func (h *firehoseHarness) FirehoseBlockCount(num uint64) int {
	return strings.Count(h.fh.GetTestingOutputBuffer().String(), fmt.Sprintf("FIRE BLOCK %d ", num))
}

// FirehoseBlock returns the decoded Firehose block printed for block `num`.
func (h *firehoseHarness) FirehoseBlock(num uint64) *pbeth.Block {
	h.t.Helper()

	return decodeFirehoseBlock(h.t, h.fh.GetTestingOutputBuffer().String(), num)
}

// stateSyncTrace returns the unique merged system transaction trace of the block (the one Firehose emits
// with null from/to for the state-sync events), nil if there is none.
func stateSyncTrace(t *testing.T, block *pbeth.Block) *pbeth.TransactionTrace {
	t.Helper()

	var found *pbeth.TransactionTrace
	for _, trace := range block.TransactionTraces {
		if len(trace.Calls) > 0 && len(trace.From) == 20 && isZeroBytes(trace.From) && isZeroBytes(trace.To) {
			require.Nil(t, found, "block %d has more than one merged system transaction", block.Number)
			found = trace
		}
	}
	return found
}

func isZeroBytes(in []byte) bool {
	for _, b := range in {
		if b != 0 {
			return false
		}
	}
	return true
}

// assertTraceOrdinalsSane checks the ordinals of every transaction trace and of its calls are well ordered,
// i.e. begin <= end for each, calls within their transaction bounds, and transactions in strictly
// increasing begin ordinals. The last one does not hold when a span commit (executed first) is moved after
// the merged state-sync transaction, see assertTraceOrdinalsSaneUnordered.
func assertTraceOrdinalsSane(t *testing.T, block *pbeth.Block) {
	t.Helper()
	assertTraceOrdinals(t, block, true)
}

// assertTraceOrdinalsSaneUnordered is assertTraceOrdinalsSane without the ordering between transactions.
func assertTraceOrdinalsSaneUnordered(t *testing.T, block *pbeth.Block) {
	t.Helper()
	assertTraceOrdinals(t, block, false)
}

func assertTraceOrdinals(t *testing.T, block *pbeth.Block, ordered bool) {
	t.Helper()

	var lastEnd uint64
	for i, trace := range block.TransactionTraces {
		require.Equal(t, uint32(i), trace.Index, "transaction trace index must be its position in the block")
		require.LessOrEqual(t, trace.BeginOrdinal, trace.EndOrdinal, "trx %d ordinals", i)
		if ordered && i > 0 {
			require.Greater(t, trace.BeginOrdinal, lastEnd, "trx %d must begin after trx %d ended", i, i-1)
		}
		lastEnd = trace.EndOrdinal

		for _, call := range trace.Calls {
			require.LessOrEqual(t, call.BeginOrdinal, call.EndOrdinal, "trx %d call %d ordinals", i, call.Index)
			require.GreaterOrEqual(t, call.BeginOrdinal, trace.BeginOrdinal, "trx %d call %d begins before its trx", i, call.Index)
			require.LessOrEqual(t, call.EndOrdinal, trace.EndOrdinal, "trx %d call %d ends after its trx", i, call.Index)
		}
	}
}
