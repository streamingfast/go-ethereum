package tracers

import (
	"bytes"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// The state-sync receipt of a block is only known to the Firehose wrapper (OnStateSyncReceipt). It must
// never outlive the run of the block that produced it: neither a failed run that was closed with
// OnBlockEnd(err) and re-opened by a retry, nor a following block.
func TestFirehose_PolygonStateSyncReceiptScope(t *testing.T) {
	stateSyncHash := common.HexToHash("0xabcd")
	stateSyncReceipt := &types.Receipt{Type: types.StateSyncTxType, TxHash: stateSyncHash, Status: types.ReceiptStatusSuccessful, CumulativeGasUsed: 7}

	newTracer := func(t *testing.T) (*Firehose, *tracing.Hooks) {
		tracer, err := NewFirehoseFromRawJSON([]byte(`{"_private":{"flushToTestBuffer":true}}`))
		require.NoError(t, err)
		hooks := tracer.TracingHooks()
		hooks.OnBlockchainInit(&params.ChainConfig{ChainID: big.NewInt(137)})
		return tracer, hooks
	}

	t.Run("failed run does not leak into the retry", func(t *testing.T) {
		tracer, hooks := newTracer(t)
		header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}

		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(header)})
		runPolygonSystemTx(hooks, 0)
		hooks.OnStateSyncReceipt(nil, stateSyncReceipt)
		hooks.OnBlockEnd(assert.AnError)
		require.Empty(t, readFirehoseBlocks(t, tracer.GetTestingOutputBuffer()), "a failed run is never printed")

		// The retry executes the same block, now without state-sync receipt: the stale one must be gone.
		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(header)})
		runPolygonSystemTx(hooks, 0)
		hooks.OnBlockEnd(nil)

		blocks := readFirehoseBlocks(t, tracer.GetTestingOutputBuffer())
		require.Len(t, blocks, 1)
		require.Len(t, blocks[0].TransactionTraces, 1)
		trace := blocks[0].TransactionTraces[0]
		assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, trace.Type)
		assert.NotEqual(t, stateSyncHash.Bytes(), trace.Hash)
		assert.Equal(t, computePolygonHash(1, blocks[0].Hash), trace.Hash)
	})

	t.Run("retry uses the receipt of its own run", func(t *testing.T) {
		tracer, hooks := newTracer(t)
		header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}

		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(header)})
		runPolygonSystemTx(hooks, 0)
		hooks.OnStateSyncReceipt(nil, &types.Receipt{Type: types.StateSyncTxType, TxHash: common.HexToHash("0xdead")})
		hooks.OnBlockEnd(assert.AnError)

		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(header)})
		runPolygonSystemTx(hooks, 0)
		hooks.OnStateSyncReceipt(nil, stateSyncReceipt)
		hooks.OnBlockEnd(nil)

		blocks := readFirehoseBlocks(t, tracer.GetTestingOutputBuffer())
		require.Len(t, blocks, 1)
		require.Len(t, blocks[0].TransactionTraces, 1)
		trace := blocks[0].TransactionTraces[0]
		assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, trace.Type)
		assert.Equal(t, stateSyncHash.Bytes(), trace.Hash)
		assert.Equal(t, uint64(7), trace.Receipt.CumulativeGasUsed)
	})

	t.Run("receipt is consumed by its block", func(t *testing.T) {
		tracer, hooks := newTracer(t)

		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)})})
		runPolygonSystemTx(hooks, 0)
		hooks.OnStateSyncReceipt(nil, stateSyncReceipt)
		hooks.OnBlockEnd(nil)

		// Pre-Madhugiri style block right after: system call, but no state-sync transaction in the body
		hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2), Difficulty: big.NewInt(1)})})
		runPolygonSystemTx(hooks, 0)
		hooks.OnBlockEnd(nil)

		blocks := readFirehoseBlocks(t, tracer.GetTestingOutputBuffer())
		require.Len(t, blocks, 2)
		assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, blocks[0].TransactionTraces[0].Type)
		assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, blocks[1].TransactionTraces[0].Type)
		assert.Equal(t, computePolygonHash(2, blocks[1].Hash), blocks[1].TransactionTraces[0].Hash)
	})
}

// A span commit (system call to the validator set contract) cannot be merged with state-sync system
// transactions: it keeps its own trace, indexed after the merged one and after the regular transactions.
func TestFirehose_PolygonSpanCommitIsNotMerged(t *testing.T) {
	tracer, err := NewFirehoseFromRawJSON([]byte(`{"_private":{"flushToTestBuffer":true}}`))
	require.NoError(t, err)
	hooks := tracer.TracingHooks()
	hooks.OnBlockchainInit(&params.ChainConfig{ChainID: big.NewInt(137)})

	user, other := common.HexToAddress("0x01"), common.HexToAddress("0x02")

	hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)})})
	// Execution order of Bor: span commit, regular transaction, state-sync events
	runPolygonSystemCall(hooks, params.BorSystemAddress, common.HexToAddress("0x1000"), 1)
	hooks.OnTxStart(&tracing.VMContext{}, types.NewTx(&types.LegacyTx{Nonce: 0, To: &other, Gas: 21000}), user)
	hooks.OnEnter(0, byte(vm.CALL), user, other, nil, 21000, nil)
	hooks.OnExit(0, nil, 21000, nil, false)
	hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 21000}, nil)
	runPolygonSystemTx(hooks, 2)
	runPolygonSystemTx(hooks, 3)
	hooks.OnBlockEnd(nil)

	blocks := readFirehoseBlocks(t, tracer.GetTestingOutputBuffer())
	require.Len(t, blocks, 1)
	traces := blocks[0].TransactionTraces
	require.Len(t, traces, 3)

	assert.Equal(t, user.Bytes(), traces[0].From, "regular transaction first")
	assert.Equal(t, uint32(0), traces[0].Index)

	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, traces[1].Type, "merged state-sync transaction")
	assert.Equal(t, uint32(1), traces[1].Index)
	assert.Len(t, traces[1].Calls, 3, "artificial root call and one call per state-sync system transaction")

	assert.Equal(t, params.BorSystemAddress.Bytes(), traces[2].From, "span commit keeps its own identity")
	assert.Equal(t, common.HexToAddress("0x1000").Bytes(), traces[2].To)
	assert.Equal(t, uint32(2), traces[2].Index)
}

func runPolygonSystemTx(hooks *tracing.Hooks, nonce uint64) {
	runPolygonSystemCall(hooks, params.BorSystemAddress, common.HexToAddress("0x1001"), nonce)
}

// runPolygonSystemCall replays what consensus/bor/statefull.ApplyMessage reports to a tracer that has
// OnTxStartWithHash: a transaction with a forced hash wrapping a single call.
func runPolygonSystemCall(hooks *tracing.Hooks, from, to common.Address, nonce uint64) {
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce, To: &to, Gas: 33554432})
	hash := common.BigToHash(new(big.Int).SetUint64(0x1000 + nonce))

	hooks.OnTxStartWithHash(&tracing.VMContext{}, tx, from, hash)
	hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 33554432, nil)
	hooks.OnExit(0, nil, 100, nil, false)
	hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 100, TxHash: hash}, nil)
}

func readFirehoseBlocks(t *testing.T, output *bytes.Buffer) []*pbeth.Block {
	t.Helper()

	var blocks []*pbeth.Block
	for line := range strings.SplitSeq(output.String(), "\n") {
		if !strings.HasPrefix(line, "FIRE BLOCK ") {
			continue
		}

		fields := strings.Split(line, " ")
		payload, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
		require.NoError(t, err)

		block := new(pbeth.Block)
		require.NoError(t, proto.Unmarshal(payload, block))
		blocks = append(blocks, block)
	}

	return blocks
}
