package tracers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
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
	"golang.org/x/exp/maps"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func Test_validateKnownTransactionTypes(t *testing.T) {
	tests := []struct {
		name      string
		txType    byte
		knownType bool
		want      error
	}{
		{"legacy", 0, true, nil},
		{"access_list", 1, true, nil},
		{"inexistant", 255, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFirehoseKnownTransactionType(tt.txType, tt.knownType)
			if tt.want == nil && err != nil {
				t.Fatalf("Transaction of type %d expected to validate properly but received error %q", tt.txType, err)
			} else if tt.want != nil && err == nil {
				t.Fatalf("Transaction of type %d expected to validate improperly but generated no error", tt.txType)
			} else if tt.want != nil && err != nil && tt.want.Error() != err.Error() {
				t.Fatalf("Transaction of type %d expected to validate improperly but generated error %q does not match expected error %q", tt.txType, err, tt.want)
			}
		})
	}
}

var ignorePbFieldNames = map[string]bool{
	"Hash":            true,
	"TotalDifficulty": true,
	"state":           true,
	"unknownFields":   true,
	"sizeCache":       true,

	// This was a Polygon specific field that existed for a while and has since been
	// removed. It can be safely ignored in all protocols now.
	"TxDependency": true,

	// Morph specific field.
	"MorphNextL1MsgIndex": true,

	// Full EIP-7928 block access list, not part of types.Header.
	"BlockAccessListRlp": true,
}

var pbFieldNameToGethMapping = map[string]string{
	"WithdrawalsRoot":  "WithdrawalsHash",
	"MixHash":          "MixDigest",
	"BaseFeePerGas":    "BaseFee",
	"StateRoot":        "Root",
	"ExtraData":        "Extra",
	"Timestamp":        "Time",
	"ReceiptRoot":      "ReceiptHash",
	"TransactionsRoot": "TxHash",
	"LogsBloom":        "Bloom",
}

var (
	pbHeaderType   = reflect.TypeFor[pbeth.BlockHeader]()
	gethHeaderType = reflect.TypeFor[types.Header]()
)

func Test_TypesHeader_AllConsensusFieldsAreKnown(t *testing.T) {
	// This exact hash varies from protocol to protocol and also sometimes from one version to the other.
	// When adding support for a new hard-fork that adds new block header fields, it's normal that this value
	// changes. If you are sure the two struct are the same, then you can update the expected hash below
	// to the new value.
	expectedHash := common.HexToHash("3ac8177f44b6b87313ac043bed11ca38b09322c98dae7077af1eee74e5b22a3f")

	gethHeaderValue := reflect.New(gethHeaderType)
	fillAllFieldsWithNonEmptyValues(t, gethHeaderValue, reflect.VisibleFields(gethHeaderType))
	gethHeader := gethHeaderValue.Interface().(*types.Header)

	// If you hit this assertion, it means that the fields `types.Header` of go-ethereum differs now
	// versus last time this test was edited.
	//
	// It's important to understand that in Ethereum Block Header (e.g. `*types.Header`), the `Hash` is
	// actually a computed value based on the other fields in the struct, so if you change any field,
	// the hash will change also.
	//
	// On hard-fork, it happens that new fields are added, this test serves as a way to "detect" in code
	// that the expected fields of `types.Header` changed
	require.Equal(t, expectedHash, gethHeader.Hash(),
		"Geth Header Hash mistmatch, got %q but expecting %q on *types.Header:\n\nGeth Header (from fillNonDefault(new(*types.Header)))\n%s",
		gethHeader.Hash().Hex(),
		expectedHash,
		asIndentedJSON(t, gethHeader),
	)
}

func Test_convertBlockData_AmsterdamFields(t *testing.T) {
	slotNumber := uint64(42)
	blockAccessListHash := common.HexToHash("0x01")

	header := &types.Header{
		Number:              big.NewInt(1),
		Difficulty:          big.NewInt(1),
		SlotNumber:          &slotNumber,
		BlockAccessListHash: &blockAccessListHash,
	}
	data := convertBlockData(types.NewBlockWithHeader(header), header)
	require.Equal(t, &slotNumber, data.SlotNumber)
	require.Equal(t, [32]byte(blockAccessListHash), *data.BlockAccessListHash)

	header = &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}
	data = convertBlockData(types.NewBlockWithHeader(header), header)
	require.Nil(t, data.SlotNumber)
	require.Nil(t, data.BlockAccessListHash)
}

func Test_FirehoseAndGethHeaderFieldMatches(t *testing.T) {
	pbFields := filter(reflect.VisibleFields(pbHeaderType), func(f reflect.StructField) bool {
		return !ignorePbFieldNames[f.Name]
	})

	gethFields := reflect.VisibleFields(gethHeaderType)

	pbFieldCount := len(pbFields)
	gethFieldCount := len(gethFields)

	pbFieldNames := extractStructFieldNames(pbFields)
	gethFieldNames := extractStructFieldNames(gethFields)

	// If you reach this assertion, it means that the fields count in the protobuf and go-ethereum are different.
	// It is super important that you properly update the mapping from pbeth.BlockHeader to go-ethereum/core/types.Header
	// that is done in `codecHeaderToGethHeader` function in `executor/provider_statedb.go`.
	require.Equal(
		t,
		pbFieldCount,
		gethFieldCount,
		fieldsCountMistmatchMessage(t, pbFieldNames, gethFieldNames))

	for pbFieldName := range pbFieldNames {
		pbFieldRenamedName, found := pbFieldNameToGethMapping[pbFieldName]
		if !found {
			pbFieldRenamedName = pbFieldName
		}

		assert.Contains(t, gethFieldNames, pbFieldRenamedName, "pbField.Name=%q (original %q) not found in gethFieldNames", pbFieldRenamedName, pbFieldName)
	}
}

var endsWithUnknownConstant = regexp.MustCompile(`.*\(\d+\)$`)

func TestFirehose_BalanceChangeAllMappedCorrectly(t *testing.T) {
	for i := 0; i <= math.MaxUint8; i++ {
		tracingReason := tracing.BalanceChangeReason(i)
		if tracingReason == tracing.BalanceChangeUnspecified || tracingReason == tracing.BalanceChangeRevert {
			// Should never happen in Firehose tracer, only if tracer is wrapped with [tracing.WrapWithJournal]
			continue
		}

		if isArbitrumSpecificReason(tracingReason) {
			// Arbitrum specific reasons are mapped properly
			continue
		}

		// Here, we leverage the fact that the `tracing.BalanceChangeReason` Stringer will render the String
		// as `<EnumName>(<indexValue>)` if the index is not mapped to a constant in the enum. If this happens,
		// we know it's not a defined constant in the Geth tracing package.
		//
		// Otherwise, it's defined and we should have some mapping for it in the `balanceChangeReasonFromChain` function.
		//
		// There is a loophole of this technique and it's that if the code generator defining the enum Stringer is
		// not run, we will think it's an undefined constant and will miss it.
		if !endsWithUnknownConstant.MatchString(tracingReason.String()) {
			require.NotPanics(t, func() {
				balanceChangeReasonFromChain(tracingReason)
			}, "BalanceChangeReason panicked for value %v", tracingReason)
		}
	}
}

func isArbitrumSpecificReason(reason tracing.BalanceChangeReason) bool {
	switch reason {
	case tracing.BalanceChangeDuringEVMExecution,
		tracing.BalanceIncreaseDeposit,
		tracing.BalanceDecreaseWithdrawToL1,
		tracing.BalanceIncreaseL1PosterFee,
		tracing.BalanceIncreaseInfraFee,
		tracing.BalanceIncreaseNetworkFee,
		tracing.BalanceChangeTransferInfraRefund,
		tracing.BalanceChangeTransferNetworkRefund,
		tracing.BalanceIncreasePrepaid,
		tracing.BalanceDecreaseUndoRefund,
		tracing.BalanceChangeEscrowTransfer,
		tracing.BalanceChangeTransferBatchposterReward,
		tracing.BalanceChangeTransferBatchposterRefund,
		tracing.BalanceChangeTransferRetryableExcessRefund,
		tracing.BalanceChangeMultiGasRefund,

		tracing.BalanceChangeTransferActivationFee,
		tracing.BalanceChangeTransferActivationReimburse,

		tracing.BalanceIncreaseMintNativeToken,
		tracing.BalanceDecreaseBurnNativeToken:
		return true
	default:
		return false
	}
}

func fillAllFieldsWithNonEmptyValues(t *testing.T, structValue reflect.Value, fields []reflect.StructField) {
	t.Helper()

	for _, field := range fields {
		fieldValue := structValue.Elem().FieldByName(field.Name)
		require.True(t, fieldValue.IsValid(), "field %q not found", field.Name)

		switch fieldValue.Interface().(type) {
		case []byte:
			fieldValue.Set(reflect.ValueOf([]byte{1}))
		case uint64:
			fieldValue.Set(reflect.ValueOf(uint64(1)))
		case *uint64:
			var mockValue uint64 = 1
			fieldValue.Set(reflect.ValueOf(&mockValue))
		case *common.Hash:
			var mockValue common.Hash = common.HexToHash("0x01")
			fieldValue.Set(reflect.ValueOf(&mockValue))
		case common.Hash:
			fieldValue.Set(reflect.ValueOf(common.HexToHash("0x01")))
		case common.Address:
			fieldValue.Set(reflect.ValueOf(common.HexToAddress("0x01")))
		case types.Bloom:
			fieldValue.Set(reflect.ValueOf(types.BytesToBloom([]byte{1})))
		case types.BlockNonce:
			fieldValue.Set(reflect.ValueOf(types.EncodeNonce(1)))
		case *big.Int:
			fieldValue.Set(reflect.ValueOf(big.NewInt(1)))
		case *pbeth.BigInt:
			fieldValue.Set(reflect.ValueOf(&pbeth.BigInt{Bytes: []byte{1}}))
		case *timestamppb.Timestamp:
			fieldValue.Set(reflect.ValueOf(&timestamppb.Timestamp{Seconds: 1}))
		default:
			// If you reach this panic in test, simply add a case above with a sane non-default
			// value for the type in question.
			t.Fatalf("unsupported type %T", fieldValue.Interface())
		}
	}
}

func fieldsCountMistmatchMessage(t *testing.T, pbFieldNames map[string]bool, gethFieldNames map[string]bool) string {
	t.Helper()

	pbRemappedFieldNames := make(map[string]bool, len(pbFieldNames))
	for pbFieldName := range pbFieldNames {
		pbFieldRenamedName, found := pbFieldNameToGethMapping[pbFieldName]
		if !found {
			pbFieldRenamedName = pbFieldName
		}

		pbRemappedFieldNames[pbFieldRenamedName] = true
	}

	return fmt.Sprintf(
		"Field count mistmatch between `pbeth.BlockHeader` (has %d fields) and `*types.Header` (has %d fields)\n\n"+
			"Fields in `pbeth.Blockheader`:\n%s\n\n"+
			"Fields in `*types.Header`:\n%s\n\n"+
			"Missing in `pbeth.BlockHeader`:\n%s\n\n"+
			"Missing in `*types.Header`:\n%s",
		len(pbRemappedFieldNames),
		len(gethFieldNames),
		asIndentedJSON(t, maps.Keys(pbRemappedFieldNames)),
		asIndentedJSON(t, maps.Keys(gethFieldNames)),
		asIndentedJSON(t, missingInSet(gethFieldNames, pbRemappedFieldNames)),
		asIndentedJSON(t, missingInSet(pbRemappedFieldNames, gethFieldNames)),
	)
}

func asIndentedJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)

	return string(out)
}

func missingInSet(a, b map[string]bool) []string {
	missing := make([]string, 0)
	for name := range a {
		if !b[name] {
			missing = append(missing, name)
		}
	}

	return missing
}

func extractStructFieldNames(fields []reflect.StructField) map[string]bool {
	result := make(map[string]bool, len(fields))
	for _, field := range fields {
		result[field.Name] = true
	}
	return result
}

func filter[S ~[]T, T any](s S, f func(T) bool) (out S) {
	out = make(S, 0, len(s)/4)
	for i, v := range s {
		if f(v) {
			out = append(out, s[i])
		}
	}

	return out
}

var from, to = common.HexToAddress("01"), common.HexToAddress("02")

func newTestFirehose(t *testing.T) (*Firehose, *tracing.Hooks) {
	t.Helper()

	f := NewFirehose(&FirehoseConfig{
		private: &privateFirehoseConfig{FlushToTestBuffer: true},
	})
	hooks := f.TracingHooks()
	hooks.OnBlockchainInit(params.TestChainConfig)

	return f, hooks
}

func blockEvent(height uint64) tracing.BlockEvent {
	return tracing.BlockEvent{
		Block: types.NewBlockWithHeader(&types.Header{
			Number:     big.NewInt(int64(height)),
			Difficulty: big.NewInt(1),
		}),
	}
}

func readSingleFirehoseBlock(t *testing.T, f *Firehose) *pbeth.Block {
	t.Helper()

	var blocks []*pbeth.Block
	for _, line := range strings.Split(f.GetTestingOutputBuffer().String(), "\n") {
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

	require.Len(t, blocks, 1)
	return blocks[0]
}

// Regression guard for the arbitrum-simulated transaction types (deposit / submit-retryable /
// internal).
//
// For those types the adapter opens the transaction's root call itself in OnTxStart. Nitro's
// TxProcessor then records the actual ArbOS execution as a *nested* frame by firing an
// OnEnter/OnExit pair at depth 0 while that simulated root is still active: `startTracer` for
// deposit/internal/submit, and the auto-redeem `MockCall` for submit-retryable. The output is
// therefore TWO calls: the empty simulated root plus the nested execution frame carrying the
// real gas/state.
func TestFirehose_ArbitrumSimulatedRoot_KeepsNestedExecutionFrame(t *testing.T) {
	f, hooks := newTestFirehose(t)
	hooks.OnBlockStart(blockEvent(100))

	tx := types.NewTx(&types.ArbitrumSubmitRetryableTx{
		ChainId:          big.NewInt(1),
		RequestId:        common.Hash{},
		From:             from,
		L1BaseFee:        big.NewInt(0),
		DepositValue:     big.NewInt(0),
		GasFeeCap:        big.NewInt(0),
		Gas:              0,
		RetryTo:          &to,
		RetryValue:       big.NewInt(0),
		Beneficiary:      to,
		MaxSubmissionFee: big.NewInt(0),
		FeeRefundAddr:    to,
		RetryData:        []byte{0x6b, 0xf6, 0xa4, 0x2d},
	})
	hooks.OnTxStart(&tracing.VMContext{}, tx, from)

	hooks.OnEnter(0, byte(vm.CALL), from, to, tx.Data(), 163200, big.NewInt(0))
	hooks.OnExit(0, nil, 163200, nil, false)

	hooks.OnTxEnd(&types.Receipt{
		Type:             types.ArbitrumSubmitRetryableTxType,
		Status:           types.ReceiptStatusSuccessful,
		GasUsed:          163200,
		TransactionIndex: 0,
	}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, f)
	require.Len(t, block.TransactionTraces, 1)
	trace := block.TransactionTraces[0]

	// The core invariant: simulated root + nested execution frame = two calls, never collapsed.
	require.Len(t, trace.Calls, 2, "simulated arbitrum root must keep its nested execution frame (a collapse to one call is the regression)")

	root, nested := trace.Calls[0], trace.Calls[1]
	assert.Equal(t, uint32(0), root.Depth, "root call must be at depth 0")
	assert.Equal(t, uint32(1), nested.Depth, "execution frame must be nested one level under the root")
	assert.Equal(t, root.Index, nested.ParentIndex, "the nested frame's parent must be the simulated root")
	assert.Equal(t, uint64(163200), nested.GasConsumed, "the real gas must stay on the nested frame, not merge into the empty root")
	assert.Equal(t, pbeth.TransactionTrace_TRX_TYPE_ARBITRUM_SUBMIT_RETRYABLE, trace.Type)
}

// TestFirehose_TxSkippedByRevertedTxHookSynthesizesRootCall covers Arbitrum transactions
// skipped by `TxProcessor.RevertedTxHook`: the EVM never runs so no call is ever recorded,
// but the transaction still gets a failed receipt consuming all gas (Robinhood Chain 4663,
// block 604227).
func TestFirehose_TxSkippedByRevertedTxHookSynthesizesRootCall(t *testing.T) {
	f, hooks := newTestFirehose(t)
	hooks.OnBlockStart(blockEvent(604227))

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(120000000),
		Gas:      100000,
		To:       &to,
		Value:    big.NewInt(999),
	})
	hooks.OnTxStart(&tracing.VMContext{}, tx, from)
	hooks.OnTxEnd(&types.Receipt{
		Type:              types.LegacyTxType,
		Status:            types.ReceiptStatusFailed,
		GasUsed:           tx.Gas(),
		CumulativeGasUsed: tx.Gas(),
		TransactionIndex:  1,
	}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, f)
	require.Len(t, block.TransactionTraces, 1)
	trace := block.TransactionTraces[0]

	require.Len(t, trace.Calls, 1)
	rootCall := trace.Calls[0]

	assert.Equal(t, pbeth.CallType_CALL, rootCall.CallType)
	assert.Equal(t, from.Bytes(), rootCall.Caller)
	assert.Equal(t, to.Bytes(), rootCall.Address)
	assert.Equal(t, big.NewInt(999).Bytes(), rootCall.Value.Bytes)
	assert.Equal(t, uint64(100000), rootCall.GasLimit)
	assert.Equal(t, uint64(100000), rootCall.GasConsumed)
	assert.True(t, rootCall.StatusFailed, "synthesized root call must be failed since the receipt is failed")
	assert.True(t, rootCall.StateReverted, "synthesized root call must have its state reverted since it failed")
	assert.Equal(t, pbeth.TransactionTraceStatus_FAILED, trace.Status)

	assert.Less(t, trace.BeginOrdinal, rootCall.BeginOrdinal)
	assert.Less(t, rootCall.BeginOrdinal, rootCall.EndOrdinal)
	assert.Less(t, rootCall.EndOrdinal, trace.EndOrdinal)
}

// TestFirehose_TxSkippedByRevertedTxHookSuccessfulReceipt covers the same EVM-less shape but
// with a successful receipt, the synthesized root call must then be successful too.
func TestFirehose_TxSkippedByRevertedTxHookSuccessfulReceipt(t *testing.T) {
	f, hooks := newTestFirehose(t)
	hooks.OnBlockStart(blockEvent(604227))
	hooks.OnTxStart(&tracing.VMContext{}, types.NewTx(&types.LegacyTx{To: &to, Gas: 21000, Value: big.NewInt(1)}), from)
	hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 21000, CumulativeGasUsed: 21000}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, f)
	require.Len(t, block.TransactionTraces, 1)
	trace := block.TransactionTraces[0]

	require.Len(t, trace.Calls, 1)
	rootCall := trace.Calls[0]

	assert.False(t, rootCall.StatusFailed)
	assert.False(t, rootCall.StateReverted)
	assert.Equal(t, pbeth.TransactionTraceStatus_SUCCEEDED, trace.Status)
}

// TestFirehose_TxWithoutReceiptIsDropped covers transactions Arbitrum skips while building the
// block (e.g. filtered ones), they end without a receipt and are not part of the block.
func TestFirehose_TxWithoutReceiptIsDropped(t *testing.T) {
	f, hooks := newTestFirehose(t)
	hooks.OnBlockStart(blockEvent(100))

	hooks.OnTxStart(&tracing.VMContext{}, types.NewTx(&types.LegacyTx{Nonce: 1, To: &to, Gas: 21000}), from)
	hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 21000, big.NewInt(0))
	hooks.OnExit(0, nil, 21000, nil, false)
	hooks.OnTxEnd(nil, errors.New("filtered"))

	kept := types.NewTx(&types.LegacyTx{Nonce: 2, To: &to, Gas: 21000})
	hooks.OnTxStart(&tracing.VMContext{}, kept, from)
	hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 21000, big.NewInt(0))
	hooks.OnExit(0, nil, 21000, nil, false)
	hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 21000}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, f)
	require.Len(t, block.TransactionTraces, 1)
	assert.Equal(t, kept.Hash().Bytes(), block.TransactionTraces[0].Hash)
}

// TestFirehose_SystemCallWithinArbitrumInternalTx covers the system call ArbOS runs within its
// internal transaction, and the storage writes ArbOS makes outside of any EVM call.
func TestFirehose_SystemCallWithinArbitrumInternalTx(t *testing.T) {
	f, hooks := newTestFirehose(t)
	hooks.OnBlockStart(blockEvent(100))

	arbOS := types.ArbosAddress
	tx := types.NewTx(&types.ArbitrumInternalTx{ChainId: big.NewInt(1), Data: []byte{0x01}})
	hooks.OnTxStart(&tracing.VMContext{}, tx, types.ArbosAddress)
	hooks.OnStorageChange(arbOS, common.HexToHash("0x01"), common.Hash{}, common.HexToHash("0x02"))

	hooks.OnSystemCallStart()
	hooks.OnEnter(0, byte(vm.CALL), params.SystemAddress, params.HistoryStorageAddress, nil, 30_000_000, big.NewInt(0))
	hooks.OnStorageChange(params.HistoryStorageAddress, common.HexToHash("0x03"), common.Hash{}, common.HexToHash("0x04"))
	hooks.OnExit(0, nil, 0, nil, false)
	hooks.OnSystemCallEnd()

	hooks.OnTxEnd(&types.Receipt{Type: types.ArbitrumInternalTxType, Status: types.ReceiptStatusSuccessful}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, f)
	require.Len(t, block.SystemCalls, 1)
	assert.Equal(t, params.HistoryStorageAddress.Bytes(), block.SystemCalls[0].Address)
	require.Len(t, block.SystemCalls[0].StorageChanges, 1)

	require.Len(t, block.TransactionTraces, 1)
	trace := block.TransactionTraces[0]
	require.Len(t, trace.Calls, 1, "the system call's call must not end up in the transaction")
	rootCall := trace.Calls[0]
	assert.Equal(t, arbOS.Bytes(), rootCall.Address)
	require.Len(t, rootCall.StorageChanges, 1)
	assert.Equal(t, arbOS.Bytes(), rootCall.StorageChanges[0].Address)
}

// TestFirehose_DelegationResolvedFromArbOS40 checks Prague rules follow the ArbOS version: Arbitrum
// headers have difficulty 1, the EVM still runs with Prague from ArbOS 40.
func TestFirehose_DelegationResolvedFromArbOS40(t *testing.T) {
	for _, tt := range []struct {
		arbOSVersion uint64
		delegates    bool
	}{
		{params.ArbosVersion_40 - 1, false},
		{params.ArbosVersion_40, true},
	} {
		t.Run(fmt.Sprintf("arbos_%d", tt.arbOSVersion), func(t *testing.T) {
			f := NewFirehose(&FirehoseConfig{private: &privateFirehoseConfig{FlushToTestBuffer: true}})
			hooks := f.TracingHooks()
			chainConfig := *params.TestChainConfig
			chainConfig.ArbitrumChainParams.EnableArbOS = true
			hooks.OnBlockchainInit(&chainConfig)

			header := &types.Header{Number: big.NewInt(100), Difficulty: big.NewInt(1), BaseFee: big.NewInt(1)}
			types.HeaderInfo{ArbOSFormatVersion: tt.arbOSVersion}.UpdateHeaderWithInfo(header)
			hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(header)})

			delegate := common.HexToAddress("0x03")
			state := &delegationStateDB{code: types.AddressToDelegation(delegate)}
			hooks.OnTxStart(&tracing.VMContext{StateDB: state}, types.NewTx(&types.LegacyTx{To: &to, Gas: 21000}), from)
			hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 21000, big.NewInt(0))
			hooks.OnExit(0, nil, 21000, nil, false)
			hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 21000}, nil)
			hooks.OnBlockEnd(nil)

			rootCall := readSingleFirehoseBlock(t, f).TransactionTraces[0].Calls[0]
			if tt.delegates {
				assert.Equal(t, delegate.Bytes(), rootCall.AddressDelegatesTo)
			} else {
				assert.Nil(t, rootCall.AddressDelegatesTo)
			}
		})
	}
}

// delegationStateDB returns the same code for every account
type delegationStateDB struct {
	tracing.StateDB
	code []byte
}

func (s *delegationStateDB) GetCode(common.Address) []byte { return s.code }
