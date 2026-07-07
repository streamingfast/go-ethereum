package tracers

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nitro v3.11.2 added TxProcessor.emitSkippedCallFrame (arbos/tx_processor.go), which fires a
// depth-0 OnEnter/OnExit pair for transaction paths that skip EVM execution. These two tests
// pin down its interaction with the Firehose root-call bookkeeping.

// For the arbitrum-simulated tx types (deposit / submit-retryable / internal) Firehose already
// opens the root call in OnTxStart. The synthetic frame must therefore be ignored, otherwise the
// deposit ends up with a duplicated nested root call.
func TestFirehose_ArbitrumSkippedFrame_DepositDoesNotDoubleRoot(t *testing.T) {
	f := NewFirehose(&FirehoseConfig{
		private: &privateFirehoseConfig{FlushToTestBuffer: true},
	})

	f.OnBlockchainInit(params.TestChainConfig)
	f.OnBlockStart(blockEvent(100))

	tx := types.NewTx(&types.ArbitrumDepositTx{
		ChainId: big.NewInt(1),
		From:    from,
		To:      to,
		Value:   big.NewInt(999),
	})

	f.OnTxStart(&tracing.VMContext{}, tx, from)

	// emitSkippedCallFrame(to, 0, err) on the filtered-deposit recipient error path.
	depErr := errors.New("filtered deposit recipient error")
	f.OnCallEnter(0, byte(vm.CALL), from, to, tx.Data(), tx.Gas(), tx.Value())
	f.OnCallExit(0, nil, 0, vm.VMErrorFromErr(depErr), true)

	receipt := &types.Receipt{
		Type:             types.ArbitrumDepositTxType,
		Status:           types.ReceiptStatusFailed,
		TransactionIndex: 0,
	}

	require.NotPanics(t, func() { f.OnTxEnd(receipt, nil) })

	require.Len(t, f.block.TransactionTraces, 1)
	trace := f.block.TransactionTraces[0]
	require.Len(t, trace.Calls, 1, "deposit must keep a single root call, not a duplicated nested one")
	assert.Equal(t, uint32(0), trace.Calls[0].Depth)

	require.NotPanics(t, func() { f.OnBlockEnd(nil) })
}

// For every other tx type OnTxStart opens no root call, so the emitSkippedCallFrame frame is the
// legitimate root and must be recorded (the OnTxEnd default synthesis then backs off since a call
// already exists). Covers the RevertedTxHook filtered/reverted and ArbitrumRetryTx error paths.
func TestFirehose_ArbitrumSkippedFrame_NonSimulatedTypeRecordsRoot(t *testing.T) {
	f := NewFirehose(&FirehoseConfig{
		private: &privateFirehoseConfig{FlushToTestBuffer: true},
	})

	f.OnBlockchainInit(params.TestChainConfig)
	f.OnBlockStart(blockEvent(100))

	tx := types.NewTx(&types.LegacyTx{
		Nonce: 1, GasPrice: big.NewInt(1), Gas: 21000, To: &to, Value: big.NewInt(1),
	})
	f.OnTxStart(&tracing.VMContext{}, tx, from)

	filteredErr := errors.New("filtered tx")
	f.OnCallEnter(0, byte(vm.CALL), from, to, tx.Data(), tx.Gas(), tx.Value())
	f.OnCallExit(0, nil, 21000, vm.VMErrorFromErr(filteredErr), true)

	receipt := &types.Receipt{
		Type:             types.LegacyTxType,
		Status:           types.ReceiptStatusFailed,
		GasUsed:          21000,
		TransactionIndex: 0,
	}

	require.NotPanics(t, func() { f.OnTxEnd(receipt, nil) })

	require.Len(t, f.block.TransactionTraces, 1)
	trace := f.block.TransactionTraces[0]
	require.Len(t, trace.Calls, 1, "the skipped frame must be recorded as the single root call")
	rootCall := trace.Calls[0]
	assert.Equal(t, uint32(0), rootCall.Depth)
	assert.Equal(t, pbeth.CallType_CALL, rootCall.CallType)
	assert.True(t, rootCall.StatusFailed)
	assert.Equal(t, uint64(21000), rootCall.GasConsumed)

	require.NotPanics(t, func() { f.OnBlockEnd(nil) })
}
