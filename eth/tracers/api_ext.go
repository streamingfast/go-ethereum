package tracers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (api *API) TraceFirehoseBlockByNumber(
	ctx context.Context,
	number rpc.BlockNumber,
	config *TraceConfig,
) (interface{}, error) {
	block, err := api.blockByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	return api.traceFirehoseBlock(ctx, block, config)
}

func (api *API) TraceFirehoseBlockByHash(
	ctx context.Context,
	hash common.Hash,
	config *TraceConfig,
) (interface{}, error) {
	block, err := api.blockByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	return api.traceFirehoseBlock(ctx, block, config)
}

func (api *API) traceFirehoseBlock(ctx context.Context, block *types.Block, config *TraceConfig) (*pbbstream.Block, error) {
	if config != nil && config.Tracer != nil && *config.Tracer != "firehose" {
		return nil, fmt.Errorf("TraceFirehoseBlockByHash only supports tracer: 'firehose'")
	}

	// Firehose tracer configuration
	firehoseTracer := NewFirehose(&FirehoseConfig{
		private: &privateFirehoseConfig{
			FlushToTestBuffer: true,
		},
	})
	hooks := firehoseTracer.TracingHooks()
	hooks.OnBlockchainInit(api.backend.ChainConfig())

	if block.NumberU64() == 0 {
		alloc, err := getGenesisState(api.backend.ChainDb(), block.Hash())
		if err != nil {
			return nil, fmt.Errorf("failed to get genesis state: %w", err)
		}
		if alloc == nil {
			return nil, errors.New("genesis allocation not found")
		}
		hooks.OnGenesisBlock(block, alloc)
	} else {
		// Prepare base state
		parent, err := api.blockByNumberAndHash(ctx, rpc.BlockNumber(block.NumberU64()-1), block.ParentHash())
		if err != nil {
			return nil, err
		}
		reexec := defaultTraceReexec
		if config != nil && config.Reexec != nil {
			reexec = *config.Reexec
		}
		statedb, release, err := api.backend.StateAtBlock(ctx, parent, reexec, nil, true, false)
		if err != nil {
			return nil, err
		}
		defer release()

		// Start block tracing
		hooks.OnBlockStart(tracing.BlockEvent{Block: block})

		// Create processor
		procInterrupt := func() bool {
			select {
			case <-ctx.Done():
				return true
			default:
				return false
			}
		}

		chainConfig := api.backend.ChainConfig()
		headerChain, err := core.NewHeaderChain(api.backend.ChainDb(), chainConfig, api.backend.Engine(), procInterrupt)
		if err != nil {
			return nil, fmt.Errorf("failed to create header chain: %w", err)
		}

		processor := core.NewStateProcessor(chainConfig, headerChain)
		vmConfig := vm.Config{Tracer: hooks}
		_, err = processor.Process(block, statedb, vmConfig)
		if err != nil {
			return nil, fmt.Errorf("block processing failed: %w", err)
		}

		// Finalize and capture block
		hooks.OnBlockEnd(nil)
	}

	outputBuffer := firehoseTracer.GetTestingOutputBuffer()
	if outputBuffer == nil {
		return nil, errors.New("testing buffer is not available")
	}

	respStr := outputBuffer.String()
	lines := strings.Split(respStr, "\n")

	var fireBlockLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "FIRE BLOCK") {
			fireBlockLine = line
			break
		}
	}

	if fireBlockLine == "" {
		return nil, fmt.Errorf("no FIRE BLOCK line found in block %d", block.Number())
	}

	// Extract the base-64 encoded protobuf block
	fields := strings.Fields(fireBlockLine)
	if len(fields) < 9 {
		return nil, fmt.Errorf("malformed FIRE BLOCK line in block %d", block.Number())
	}

	blockPayloadB64 := fields[len(fields)-1]
	blockBytes, err := base64.StdEncoding.DecodeString(blockPayloadB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode FIRE BLOCK payload: %w", err)
	}

	// Unmarshal the traced Ethereum block
	ethBlock := &pbeth.Block{}
	if err := proto.Unmarshal(blockBytes, ethBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal eth block: %w", err)
	}

	payload, err := anypb.New(ethBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to wrap eth block: %w", err)
	}

	bstreamBlock := &pbbstream.Block{
		Number:    ethBlock.Number,
		Id:        ethBlock.GetFirehoseBlockID(),
		ParentId:  ethBlock.GetFirehoseBlockParentID(),
		Timestamp: timestamppb.New(ethBlock.GetFirehoseBlockTime()),
		LibNum:    ethBlockLIBNum(ethBlock),
		ParentNum: ethBlock.GetFirehoseBlockParentNumber(),
		Payload:   payload,
	}

	return bstreamBlock, nil
}

func ethBlockLIBNum(b *pbeth.Block) uint64 {
	if b.Number == 0 {
		return 0
	}

	// TODO: fetch the finalized block from the api backend directly
	return b.Number - 1
}

// getGenesisState from core is unexported. Therefore, we rewrite the function here
func getGenesisState(db ethdb.Database, blockhash common.Hash) (alloc types.GenesisAlloc, err error) {
	blob := rawdb.ReadGenesisStateSpec(db, blockhash)
	if len(blob) != 0 {
		if err := alloc.UnmarshalJSON(blob); err != nil {
			return nil, err
		}

		return alloc, nil
	}

	var genesis *core.Genesis
	switch blockhash {
	case params.MainnetGenesisHash:
		genesis = core.DefaultGenesisBlock()
	case params.SepoliaGenesisHash:
		genesis = core.DefaultSepoliaGenesisBlock()
	case params.HoleskyGenesisHash:
		genesis = core.DefaultHoleskyGenesisBlock()
	}
	if genesis != nil {
		return genesis.Alloc, nil
	}

	return nil, nil
}
