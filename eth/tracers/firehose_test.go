package tracers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
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

	// EIP-7843 (Amsterdam) field, not in Bor's header
	"SlotNumber": true,
}

var ignoreGethFieldNames = map[string]bool{
	// Those are fields used internally by the polygon miners
	"ActualTime":    true,
	"AbortRecovery": true,
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
	expectedHash := common.HexToHash("4ced4916132bbf6a7819a310bbac4abf354062a00efc980ea4f0bab406546ac5")

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
		"Geth Header Hash mismatch, got %q but expecting %q on *types.Header:\n\nGeth Header (from fillNonDefault(new(*types.Header)))\n%s",
		gethHeader.Hash().Hex(),
		expectedHash,
		asIndentedJSON(t, gethHeader),
	)
}

func Test_FirehoseAndGethHeaderFieldMatches(t *testing.T) {
	pbFields := filter(reflect.VisibleFields(pbHeaderType), func(f reflect.StructField) bool {
		return !ignorePbFieldNames[f.Name]
	})

	gethFields := filter(reflect.VisibleFields(gethHeaderType), func(f reflect.StructField) bool {
		return !ignoreGethFieldNames[f.Name]
	})

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
		fieldsCountMismatchMessage(t, pbFieldNames, gethFieldNames))

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

func fillAllFieldsWithNonEmptyValues(t *testing.T, structValue reflect.Value, fields []reflect.StructField) {
	t.Helper()

	for _, field := range fields {
		fieldValue := structValue.Elem().FieldByName(field.Name)
		require.True(t, fieldValue.IsValid(), "field %q not found", field.Name)

		switch fieldValue.Interface().(type) {
		case []byte:
			fieldValue.Set(reflect.ValueOf([]byte{1}))
		case bool:
			fieldValue.Set(reflect.ValueOf(true))
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
		case time.Time:
			fieldValue.Set(reflect.ValueOf(time.Unix(0, 1)))
		default:
			// If you reach this panic in test, simply add a case above with a sane non-default
			// value for the type in question.
			t.Fatalf("unsupported type %T", fieldValue.Interface())
		}
	}
}

func fieldsCountMismatchMessage(t *testing.T, pbFieldNames map[string]bool, gethFieldNames map[string]bool) string {
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

func TestFirehose_PolygonFeeTransferLog(t *testing.T) {
	from, to := common.HexToAddress("0x01"), common.HexToAddress("0x02")
	feeLog := &types.Log{
		Address: core.GetFeeAddress(),
		Topics:  []common.Hash{core.GetTransferFeeLogSig(), {}, {}, {}},
		Index:   3,
	}

	tracer, err := NewFirehoseFromRawJSON([]byte(`{"_private":{"flushToTestBuffer":true}}`))
	require.NoError(t, err)
	hooks := tracer.TracingHooks()

	hooks.OnBlockchainInit(&params.ChainConfig{ChainID: big.NewInt(137)})
	hooks.OnBlockStart(tracing.BlockEvent{Block: types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)})})
	hooks.OnTxStart(&tracing.VMContext{}, types.NewTx(&types.LegacyTx{To: &to, Gas: 21000}), from)
	hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 21000, nil)
	hooks.OnLog(&types.Log{Address: to, Index: 2})
	hooks.OnExit(0, nil, 21000, vm.ErrExecutionReverted, true)
	// Bor emits the fee transfer log after the root call ended
	hooks.OnLog(feeLog)
	hooks.OnTxEnd(&types.Receipt{Status: types.ReceiptStatusFailed, Logs: []*types.Log{feeLog}}, nil)
	hooks.OnBlockEnd(nil)

	block := readSingleFirehoseBlock(t, tracer.GetTestingOutputBuffer())
	require.Len(t, block.TransactionTraces, 1)
	trx := block.TransactionTraces[0]
	rootCall := trx.Calls[0]
	require.True(t, rootCall.StateReverted)
	require.Len(t, rootCall.Logs, 2)

	assert.Equal(t, uint32(0), rootCall.Logs[0].BlockIndex, "reverted log loses its block index")
	assert.Equal(t, uint32(3), rootCall.Logs[1].BlockIndex, "fee transfer log keeps its block index")

	require.Len(t, trx.Receipt.Logs, 1)
	assert.Equal(t, rootCall.Logs[1].Ordinal, trx.Receipt.Logs[0].Ordinal)
	assert.Equal(t, uint32(3), trx.Receipt.Logs[0].BlockIndex)
}

func readSingleFirehoseBlock(t *testing.T, output *bytes.Buffer) *pbeth.Block {
	t.Helper()

	var blocks []*pbeth.Block
	for _, line := range strings.Split(output.String(), "\n") {
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
