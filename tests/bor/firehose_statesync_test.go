//go:build integration
// +build integration

package bor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus/bor"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TestStateSyncTracing_FirehoseOutputMatchesGolden locks the *shape and bytes* of
// the Firehose protobuf block produced for a post-Madhugiri Bor block carrying a
// state-sync transaction, processed through the real production wiring
// (eth.Config.VMTrace="firehose" -> backend.go). The no-panic test
// (TestStateSyncTracing_FirehoseLiveTracerDoesNotPanic) proves it runs; this test
// proves the *output* is the pre-#2236 (v2.8.2) representation and that #2236's
// generic state-sync wrapper did NOT leak into the Firehose stream.
//
// Concretely it asserts the canonical Polygon representation:
//   - exactly one TransactionTrace of type TRX_TYPE_POLYGON_STATE_SYNC (the
//     combined system transaction), and
//   - no extra synthetic top-level transaction from WrapStateSyncHooks.
//
// and then compares the full decoded block against a golden file. Regenerate with:
//
//	GOLDEN_UPDATE=true go test -tags integration ./tests/bor -run TestStateSyncTracing_FirehoseOutputMatchesGolden
func TestStateSyncTracing_FirehoseOutputMatchesGolden(t *testing.T) {
	t.Parallel()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))
	fdlimit.Raise(2048)

	// Register a Firehose live tracer that flushes to an in-memory buffer (instead
	// of stdout) so the test can read back the produced protobuf blocks. The tracer
	// is otherwise the production "firehose" tracer; flushToTestBuffer only changes
	// the output sink, not the tracing semantics. We capture the *Firehose instance
	// so we can read its testing buffer after InsertChain.
	var fh *tracers.Firehose
	const tracerName = "firehose-statesync-capture"
	tracers.LiveDirectory.Register(tracerName, func(_ json.RawMessage) (*tracing.Hooks, error) {
		var err error
		fh, err = tracers.NewFirehoseFromRawJSON([]byte(`{"_private":{"flushToTestBuffer":true,"ignoreGenesisBlock":true}}`))
		if err != nil {
			return nil, err
		}
		return tracers.NewTracingHooksFromFirehose(fh), nil
	})

	stateSyncConfirmationDelay := int64(128)
	updateGenesis := func(gen *core.Genesis) {
		gen.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": uint64(stateSyncConfirmationDelay)}
		gen.Config.Bor.Sprint = map[string]uint64{"0": sprintSize}
		gen.Config.Bor.MadhugiriBlock = big.NewInt(0) // Madhugiri from genesis.
	}
	init := buildEthereumInstanceWithVMTrace(t, rawdb.NewMemoryDatabase(), tracerName, updateGenesis)
	chain := init.ethereum.BlockChain()
	engine := init.ethereum.Engine()
	_bor := engine.(*bor.Bor)
	defer _bor.Close()

	require.NotNil(t, fh, "firehose capture tracer must have been instantiated by eth.New")

	block := init.genesis.ToBlock()
	span0 := createMockSpan(addr, chain.Config().ChainID.String())
	borValSet := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet)
	currentValidators := borValSet.Validators

	res := loadSpanFromFile(t)
	spanner := getMockedSpanner(t, currentValidators)
	_bor.SetSpanner(spanner)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	h := createMockHeimdall(ctrl, &span0, res)

	fromID := uint64(1)
	to := int64(chain.GetHeaderByNumber(0).Time) + 9 - stateSyncConfirmationDelay
	const eventCount = 5

	sample := getSampleEventRecord(t)
	sample.Time = time.Unix(to-int64(eventCount+1), 0)
	eventRecords := generateFakeStateSyncEvents(sample, eventCount)

	h.EXPECT().StateSyncEvents(gomock.Any(), fromID, to).Return(eventRecords, nil).AnyTimes()
	h.EXPECT().GetLatestSpan(gomock.Any()).Return(nil, fmt.Errorf("span not found")).AnyTimes()
	_bor.SetHeimdallClient(h)

	for i := uint64(1); i < sprintSize; i++ {
		if IsSpanEnd(i) {
			currentValidators = borValSet.Validators
		}
		block = buildNextBlock(t, _bor, chain, block, nil, init.genesis.Config.Bor, nil, currentValidators, false, nil, nil)
		insertNewBlock(t, chain, block)
	}

	// Sprint-end block carries the state-sync tx; the import path drives Firehose.
	block = buildNextBlock(t, _bor, chain, block, nil, init.genesis.Config.Bor, nil, borValSet.Validators, false, nil, nil)
	insertNewBlock(t, chain, block)

	stateSyncBlockNum := block.NumberU64()
	require.Equal(t, uint8(types.StateSyncTxType), chain.GetBlockByNumber(stateSyncBlockNum).Transactions()[0].Type())

	got := decodeFirehoseBlock(t, fh.InternalTestingBuffer().String(), stateSyncBlockNum)

	// Shape invariant: the v2.8.2 representation is a single combined state-sync
	// transaction. If #2236's WrapStateSyncHooks had leaked in, we'd instead see a
	// synthetic top-level call frame / a different tx structure here.
	var stateSyncTraces int
	for _, trx := range got.TransactionTraces {
		if trx.Type == pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC {
			stateSyncTraces++
		}
	}
	require.Equal(t, 1, stateSyncTraces,
		"expected exactly one TRX_TYPE_POLYGON_STATE_SYNC transaction trace — the v2.8.2 combined "+
			"system transaction; more/none means the #2236 generic state-sync path leaked into Firehose")

	assertFirehoseBlockGolden(t, filepath.Join("testdata", "firehose", "StateSyncOutput"), got)
}

// decodeFirehoseBlock parses the Firehose testing buffer (newline-separated
// "FIRE BLOCK <num> <hash> <prevNum> <prevHash> <libNum> <timeNano> <base64proto>"
// lines) and returns the decoded pbeth.Block for the requested block number.
func decodeFirehoseBlock(t *testing.T, buffer string, blockNum uint64) *pbeth.Block {
	t.Helper()

	want := fmt.Sprintf("FIRE BLOCK %d ", blockNum)
	for _, line := range strings.Split(buffer, "\n") {
		if !strings.HasPrefix(line, want) {
			continue
		}
		// The protobuf payload is the last space-separated token on the line.
		idx := strings.LastIndexByte(line, ' ')
		require.Greater(t, idx, 0, "malformed FIRE BLOCK line")
		raw, err := base64.StdEncoding.DecodeString(line[idx+1:])
		require.NoError(t, err, "failed to base64-decode FIRE BLOCK payload")

		block := &pbeth.Block{}
		require.NoError(t, proto.Unmarshal(raw, block), "failed to unmarshal pbeth.Block")
		return block
	}

	require.Failf(t, "block not found", "no FIRE BLOCK line for block %d in firehose buffer", blockNum)
	return nil
}

// assertFirehoseBlockGolden compares (or, under GOLDEN_UPDATE=true, writes) the
// decoded Firehose block against goldenDir/block.<num>.golden.json. Comparison is
// done with proto.Equal on the parsed messages so protojson whitespace
// non-determinism does not matter.
func assertFirehoseBlockGolden(t *testing.T, goldenDir string, block *pbeth.Block) {
	t.Helper()

	goldenPath := filepath.Join(goldenDir, fmt.Sprintf("block.%d.golden.json", block.Number))

	if os.Getenv("GOLDEN_UPDATE") == "true" {
		require.NoError(t, os.MkdirAll(goldenDir, 0o755))
		out, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(block)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(goldenPath, out, 0o644))
		return
	}

	wantBytes, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden file %s — run with GOLDEN_UPDATE=true to create it", goldenPath)

	want := &pbeth.Block{}
	require.NoError(t, protojson.Unmarshal(wantBytes, want), "failed to parse golden file %s", goldenPath)

	if !proto.Equal(want, block) {
		got, _ := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(block)
		t.Fatalf("firehose block %d does not match golden %s\n--- got ---\n%s", block.Number, goldenPath, string(got))
	}
}
