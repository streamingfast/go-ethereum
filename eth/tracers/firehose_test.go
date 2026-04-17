package tracers

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/maps"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ignorePbFieldNames = map[string]bool{
	"Hash":            true,
	"TotalDifficulty": true,
	"state":           true,
	"unknownFields":   true,
	"sizeCache":       true,

	// This was a Polygon specific field that existed for a while and has since been
	// removed. It can be safely ignored in all protocols now.
	"TxDependency": true,
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
	expectedHash := common.HexToHash("1e995a03fe468e359956abad3de7aec9f4b52acbe417e78f75a109466a473d3c")

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
