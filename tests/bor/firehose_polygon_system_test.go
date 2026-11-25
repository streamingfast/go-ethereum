//go:build integration
// +build integration

package bor

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
)

// Pre-Madhugiri, state-sync events are applied by the engine with one system call each and there is no
// transaction in the block body. Firehose merges them in a single transaction of the legacy type whose
// hash is the one RPC serves for the Bor receipt of the block.
func TestFirehose_PreMadhugiriStateSyncBlock(t *testing.T) {
	t.Parallel()

	h := newFirehoseHarness(t, firehoseHarnessOptions{StateSyncBatches: []int{3}})

	h.AdvanceTo(sprintSize - 1)
	for num := uint64(1); num < sprintSize; num++ {
		block := h.FirehoseBlock(num)
		assert.Empty(t, block.TransactionTraces, "block %d is not a sprint-end block and has no transaction", num)
	}

	sprintEnd := h.Advance()
	require.Empty(t, sprintEnd.Transactions(), "pre-Madhugiri state-sync is not part of the block body")

	block := h.FirehoseBlock(sprintEnd.NumberU64())
	require.Len(t, block.TransactionTraces, 1)
	assertTraceOrdinalsSane(t, block)

	trace := stateSyncTrace(t, block)
	require.NotNil(t, trace, "the sprint-end block must carry the merged state-sync transaction")

	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, trace.Type)
	assert.Equal(t, pbeth.TransactionTraceStatus_SUCCEEDED, trace.Status)
	assert.Equal(t, uint32(0), trace.Index)
	assert.Equal(t,
		types.GetDerivedBorTxHash(types.BorReceiptKey(sprintEnd.NumberU64(), sprintEnd.Hash())).Bytes(),
		trace.Hash,
		"merged hash is the derived hash of the Bor receipt of the block")

	assertMergedStateSyncCalls(t, trace, 3)
}

// Post-Madhugiri, the state-sync transaction is part of the block body and its hash and receipt come from
// the chain (OnStateSyncReceipt), the merged calls are the same as before the fork.
func TestFirehose_PostMadhugiriStateSyncBlock(t *testing.T) {
	t.Parallel()

	h := newFirehoseHarness(t, firehoseHarnessOptions{MadhugiriBlock: big.NewInt(0), StateSyncBatches: []int{3}})

	sprintEnd := h.AdvanceTo(sprintSize)
	require.Len(t, sprintEnd.Transactions(), 1)
	stateSyncTx := sprintEnd.Transactions()[0]
	require.Equal(t, uint8(types.StateSyncTxType), stateSyncTx.Type())

	block := h.FirehoseBlock(sprintEnd.NumberU64())
	require.Len(t, block.TransactionTraces, 1)
	assertTraceOrdinalsSane(t, block)

	trace := stateSyncTrace(t, block)
	require.NotNil(t, trace)

	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, trace.Type)
	assert.Equal(t, stateSyncTx.Hash().Bytes(), trace.Hash, "post-Madhugiri hash is the StateSyncTx hash from the body")

	receipts := h.chain.GetReceiptsByHash(sprintEnd.Hash())
	require.Len(t, receipts, 1)
	require.NotNil(t, trace.Receipt)
	assert.Equal(t, receipts[0].CumulativeGasUsed, trace.Receipt.CumulativeGasUsed)
	assert.Equal(t, receipts[0].Bloom.Bytes(), trace.Receipt.LogsBloom)
	require.Len(t, trace.Receipt.Logs, len(receipts[0].Logs))
	for i, log := range receipts[0].Logs {
		assert.Equal(t, log.Address.Bytes(), trace.Receipt.Logs[i].Address, "log %d address", i)
		assert.Equal(t, uint32(i), trace.Receipt.Logs[i].Index, "log %d index within the receipt", i)
		assert.Equal(t, uint32(log.Index), trace.Receipt.Logs[i].BlockIndex, "log %d index within the block", i)
	}

	assertMergedStateSyncCalls(t, trace, 3)
}

// The transaction type, hash and receipt source change exactly at the Madhugiri activation block, observed
// on sprint-end blocks before (block 4), at (block 8) and after (block 12) the fork.
func TestFirehose_MadhugiriBoundary(t *testing.T) {
	t.Parallel()

	h := newFirehoseHarness(t, firehoseHarnessOptions{
		MadhugiriBlock:   big.NewInt(int64(2 * sprintSize)),
		StateSyncBatches: []int{2, 2, 2},
	})

	h.AdvanceTo(3 * sprintSize)

	before := h.FirehoseBlock(sprintSize)
	at := h.FirehoseBlock(2 * sprintSize)
	after := h.FirehoseBlock(3 * sprintSize)

	for _, block := range []*pbeth.Block{before, at, after} {
		require.Len(t, block.TransactionTraces, 1, "block %d", block.Number)
		assertTraceOrdinalsSane(t, block)
		assertMergedStateSyncCalls(t, stateSyncTrace(t, block), 2)
	}

	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, before.TransactionTraces[0].Type, "N-1 sprint end")
	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, at.TransactionTraces[0].Type, "block N")
	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, after.TransactionTraces[0].Type, "N+1 sprint end")

	for _, num := range []uint64{2 * sprintSize, 3 * sprintSize} {
		chainBlock := h.chain.GetBlockByNumber(num)
		require.Len(t, chainBlock.Transactions(), 1)
		assert.Equal(t, chainBlock.Transactions()[0].Hash().Bytes(), h.FirehoseBlock(num).TransactionTraces[0].Hash, "block %d", num)
	}
	assert.Empty(t, h.chain.GetBlockByNumber(sprintSize).Transactions())
}

// assertMergedStateSyncCalls checks the shape of the merged system transaction: an artificial top-level
// call (index 1, null caller/address) and one call per state-sync event from the Bor system address to
// the state receiver contract, children of the artificial call.
func assertMergedStateSyncCalls(t *testing.T, trace *pbeth.TransactionTrace, events int) {
	t.Helper()

	require.Len(t, trace.Calls, events+1, "artificial root call + one call per state-sync event")

	root := trace.Calls[0]
	assert.Equal(t, uint32(1), root.Index)
	assert.Equal(t, uint32(0), root.ParentIndex)
	assert.Equal(t, uint32(0), root.Depth)
	assert.True(t, isZeroBytes(root.Caller))
	assert.True(t, isZeroBytes(root.Address))

	for i, call := range trace.Calls[1:] {
		assert.Equal(t, uint32(1), call.ParentIndex, "event call %d is a child of the artificial root", i)
		assert.Equal(t, uint32(1), call.Depth, "event call %d depth", i)
		assert.Equal(t, uint32(i+2), call.Index, "event call %d must be indexed after the root, in order", i)
		assert.Equal(t, pbeth.CallType_CALL, call.CallType)
		assert.Equal(t, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}, call.Caller, "caller is the Bor system address")
		assert.Equal(t, uint32(0x1001), uint32(call.Address[18])<<8|uint32(call.Address[19]), "call to the state receiver contract")
	}
}

// A sprint-end block carrying a regular transaction and state-sync events: the system transaction is
// merged and indexed after the regular ones, the regular transaction keeps its own trace with Polygon's
// fee transfer log, and the block has no block reward balance change (Bor has no block reward).
func TestFirehose_SprintEndBlockWithRegularTransaction(t *testing.T) {
	t.Parallel()

	for name, madhugiri := range map[string]*big.Int{"pre_madhugiri": nil, "post_madhugiri": big.NewInt(0)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newFirehoseHarness(t, firehoseHarnessOptions{MadhugiriBlock: madhugiri, StateSyncBatches: []int{2}})
			h.AdvanceTo(sprintSize - 1)

			recipient := common.HexToAddress("0xbeef")
			tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
				ChainID:   h.chain.Config().ChainID,
				Nonce:     0,
				To:        &recipient,
				Value:     big.NewInt(1),
				Gas:       21000,
				GasFeeCap: newGwei(5),
				GasTipCap: big.NewInt(2),
			}), types.LatestSigner(h.chain.Config()), key)
			require.NoError(t, err)

			sprintEnd := h.Advance(tx)
			block := h.FirehoseBlock(sprintEnd.NumberU64())
			assertTraceOrdinalsSane(t, block)

			require.Len(t, block.TransactionTraces, 2, "the regular transaction and the merged state-sync one")

			regular := block.TransactionTraces[0]
			assert.Equal(t, tx.Hash().Bytes(), regular.Hash)
			assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_DYNAMIC_FEE, regular.Type)
			assert.Equal(t, pbeth.TransactionTraceStatus_SUCCEEDED, regular.Status)
			assert.Equal(t, uint32(0), regular.Index)

			var feeLogs, transferLogs int
			for _, log := range regular.Receipt.Logs {
				if !bytes.Equal(log.Address, core.GetFeeAddress().Bytes()) || len(log.Topics) != 4 {
					continue
				}
				if sig := core.GetTransferFeeLogSig(); bytes.Equal(log.Topics[0], sig[:]) {
					feeLogs++
				} else {
					transferLogs++
				}
			}
			assert.Equal(t, 1, feeLogs, "Polygon fee transfer log is part of the receipt of the regular transaction")
			assert.Equal(t, 1, transferLogs, "Polygon transfer log of the value is part of the receipt of the regular transaction")

			system := block.TransactionTraces[1]
			assert.Equal(t, uint32(1), system.Index, "system transaction is indexed after the regular ones")
			assert.Greater(t, system.BeginOrdinal, regular.EndOrdinal)
			assertMergedStateSyncCalls(t, stateSyncTrace(t, block), 2)

			wantType := pbeth.TransactionTrace_TRX_TYPE_LEGACY
			if madhugiri != nil {
				wantType = pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC
			}
			assert.Equal(t, wantType, system.Type)

			for _, change := range block.BalanceChanges {
				assert.NotEqual(t, pbeth.BalanceChange_REASON_REWARD_MINE_BLOCK, change.Reason, "Bor has no block reward")
			}
		})
	}
}

// The genesis block is printed with the allocation as balance changes and chains to block 1, and blocks
// without any transaction (the common case between sprint-end blocks) are printed without any trace.
func TestFirehose_GenesisAndEmptyBlocks(t *testing.T) {
	t.Parallel()

	h := newFirehoseHarness(t, firehoseHarnessOptions{PrintGenesisBlock: true})

	genesis := h.FirehoseBlock(0)
	assert.Equal(t, h.chain.GetHeaderByNumber(0).Hash().Bytes(), genesis.Hash)

	// The genesis allocation lives in one synthetic transaction with a single synthetic call
	require.Len(t, genesis.TransactionTraces, 1)
	require.Len(t, genesis.TransactionTraces[0].Calls, 1)
	call := genesis.TransactionTraces[0].Calls[0]
	require.NotEmpty(t, call.BalanceChanges, "genesis allocation is reported as balance changes")
	for _, change := range call.BalanceChanges {
		assert.Equal(t, pbeth.BalanceChange_REASON_GENESIS_BALANCE, change.Reason)
	}

	var codeAddresses [][]byte
	for _, change := range call.CodeChanges {
		codeAddresses = append(codeAddresses, change.Address)
	}
	assert.Contains(t, codeAddresses, common.HexToAddress("0x1000").Bytes(), "validator set contract code is part of the genesis allocation")

	h.AdvanceTo(2)

	first := h.FirehoseBlock(1)
	assert.Equal(t, h.genesis.Hash().Bytes(), first.Header.ParentHash)
	assert.Empty(t, first.TransactionTraces)
	assert.Empty(t, first.BalanceChanges, "an empty Bor block has no balance change, there is no block reward")
	assert.Empty(t, first.SystemCalls, "no system call outside of the state-sync, span commit and Cancun/Prague hooks")

	second := h.FirehoseBlock(2)
	assert.Equal(t, first.Hash, second.Header.ParentHash)
	assert.Empty(t, second.TransactionTraces)
}

// Before Rio, the first block of a sprint can also commit the next span: a system call to the validator
// set contract (0x1000) that cannot be merged with the state-sync ones. It is kept as its own transaction,
// indexed after the merged state-sync one.
func TestFirehose_PreRioSpanCommitBlock(t *testing.T) {
	t.Parallel()

	h := newFirehoseHarness(t, firehoseHarnessOptions{RealSpanner: true, StateSyncBatches: []int{2}})

	h.AdvanceTo(sprintSize)
	block := h.FirehoseBlock(sprintSize)
	assertTraceOrdinalsSaneUnordered(t, block)

	var validatorCommits, stateSyncTraces int
	for _, trace := range block.TransactionTraces {
		switch {
		case isZeroBytes(trace.To):
			stateSyncTraces++
		case bytes.Equal(trace.To, common.HexToAddress("0x1000").Bytes()):
			validatorCommits++
			assert.Equal(t, params.BorSystemAddress.Bytes(), trace.From)
		}
	}

	assert.Equal(t, 1, stateSyncTraces, "merged state-sync transaction")
	assert.Equal(t, 1, validatorCommits, "span commit transaction towards the validator set contract")
	require.Len(t, block.TransactionTraces, 2)
	assert.Equal(t, uint32(1), block.TransactionTraces[1].Index)
	assert.True(t, isZeroBytes(block.TransactionTraces[0].To), "merged state-sync transaction comes first")
	assert.True(t, bytes.Equal(block.TransactionTraces[1].To, common.HexToAddress("0x1000").Bytes()), "span commit comes last")

	// The span commit is executed before the state-sync events but printed after the merged state-sync
	// transaction, to match the order of the RPC receipts. Its ordinals therefore come before the ones of the
	// transaction listed before it.
	assert.Less(t, block.TransactionTraces[1].EndOrdinal, block.TransactionTraces[0].BeginOrdinal)
}

// A sprint-end block that fails after being executed (here a wrong state root) was traced up to
// OnBlockEnd(err) and must not be printed, nor leak its state-sync receipt, into the valid block that is
// imported next at the same height.
func TestFirehose_FailedSprintEndBlockDoesNotLeak(t *testing.T) {
	t.Parallel()

	for name, madhugiri := range map[string]*big.Int{"pre_madhugiri": nil, "post_madhugiri": big.NewInt(0)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newFirehoseHarness(t, firehoseHarnessOptions{MadhugiriBlock: madhugiri, StateSyncBatches: []int{3}})
			h.AdvanceTo(sprintSize - 1)

			wrongRoot := func(block *types.Block, _ []*types.Receipt) *types.Block {
				header := block.Header()
				header.Root = common.HexToHash("0xbad")
				return types.NewBlockWithHeader(header).WithBody(*block.Body())
			}

			bad := h.Build(wrongRoot)
			_, err := h.chain.InsertChain([]*types.Block{bad}, false)
			require.Error(t, err, "block with a wrong state root must be rejected")
			require.Zero(t, h.FirehoseBlockCount(sprintSize), "a rejected block is never printed")

			valid := h.Advance()
			require.NotEqual(t, bad.Hash(), valid.Hash())
			require.Equal(t, 1, h.FirehoseBlockCount(sprintSize))

			block := h.FirehoseBlock(sprintSize)
			assert.Equal(t, valid.Hash().Bytes(), block.Hash)
			require.Len(t, block.TransactionTraces, 1)
			assertTraceOrdinalsSane(t, block)
			assertMergedStateSyncCalls(t, stateSyncTrace(t, block), 3)

			if madhugiri == nil {
				assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_LEGACY, block.TransactionTraces[0].Type)
				assert.Equal(t, types.GetDerivedBorTxHash(types.BorReceiptKey(valid.NumberU64(), valid.Hash())).Bytes(), block.TransactionTraces[0].Hash)
			} else {
				assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC, block.TransactionTraces[0].Type)
				assert.Equal(t, valid.Transactions()[0].Hash().Bytes(), block.TransactionTraces[0].Hash)
			}
		})
	}
}
