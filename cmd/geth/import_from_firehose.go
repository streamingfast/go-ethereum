package main

import (
	"context"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/firehose-core/firehose/client"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"github.com/urfave/cli/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding/gzip"
	"io"
	"math/big"
	"os"
	"sync"
	"time"
)

func importFromFirehose(ctx *cli.Context) error {
	if ctx.Args().Len() < 2 {
		return fmt.Errorf("usage: import-from-firehose <firehose-endpoint> <chainID> [<rpc>]")
	}
	apiToken := os.Getenv("FIREHOSE_API_TOKEN")
	var endpoint, chainIDStr, externalRpc string
	externalRpc = ""

	endpoint = ctx.Args().Get(0)
	chainIDStr = ctx.Args().Get(1)

	if ctx.Args().Len() == 3 {
		externalRpc = ctx.Args().Get(2)
	}

	batchSize := ctx.Int("batch-size")
	endBlock := ctx.Uint64("end-block")
	workerCount := ctx.Int("worker-count")
	bufferSize := ctx.Int("firehose-buffer-size")

	chainID := new(big.Int)
	if _, ok := chainID.SetString(chainIDStr, 10); !ok {
		return fmt.Errorf("invalid chainID: %s", chainIDStr)
	}

	// Open Geth stack and chain
	stack, cfg := makeConfigNode(ctx)
	defer stack.Close()
	utils.SetupMetrics(&cfg.Metrics)
	chain, db := utils.MakeChain(ctx, stack, false)
	defer db.Close()
	defer chain.Stop()

	var startBlock int
	if ctx.IsSet("start-block") {
		startBlock = ctx.Int("start-block")
		if startBlock < 0 {
			return fmt.Errorf("startBlock must be non-negative")
		}
		fmt.Printf("Starting from user-specified block: %d\n", startBlock)
	} else {
		// Resume from the local chain head + 1
		head := chain.CurrentBlock()
		if head != nil {
			startBlock = int(head.Number.Uint64() + 1)
			fmt.Printf("No start block specified. Resuming from last imported block: %d\n", startBlock)
		} else {
			startBlock = 0 // start from genesis
			fmt.Println("No start block specified and no local chain found. Starting from genesis (block 0).")
		}
	}

	var totalBlocks int
	currentBlock := startBlock
	err := processFirehoseBlocksWithReconnect(endpoint, apiToken, &currentBlock, endBlock, batchSize, workerCount, bufferSize, chainID, externalRpc, func(blocks []*types.Block, batchNum int) error {
		if len(blocks) == 0 {
			return nil
		}
		firstNum := blocks[0].NumberU64()
		lastNum := blocks[len(blocks)-1].NumberU64()
		if _, err := chain.InsertChain(blocks); err != nil {
			fmt.Printf("failed to import batch %d (blocks %d-%d): %v\n", batchNum, firstNum, lastNum, err)
			return err
		}
		fmt.Printf("Imported batch %d of %d blocks (blocks %d-%d)\n", batchNum, len(blocks), firstNum, lastNum)
		totalBlocks += len(blocks)
		currentBlock = int(lastNum + 1)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Import completed successfully. Total blocks imported: %d\n", totalBlocks)
	return nil
}

func processFirehoseBlocksWithReconnect(
	endpoint string,
	apiToken string,
	startBlock *int,
	endBlock uint64,
	batchSize int,
	workerCount int,
	bufferSize int,
	chainID *big.Int,
	externalRpc string,
	handler func(blocks []*types.Block, batchNum int) error,
) error {
	client, closeFunc, grpcOpts, err := client.NewFirehoseClient(endpoint, apiToken, "", false, false)
	if err != nil {
		return fmt.Errorf("failed to create Firehose client: %w", err)
	}
	defer closeFunc()
	grpcOpts = append(grpcOpts, grpc.UseCompressor(gzip.Name))

	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 0
	backOff := backoff.WithContext(bo, context.Background())

	for {
		prev := *startBlock

		err := processFirehoseBlocksWithClient(client, grpcOpts, startBlock, endBlock, batchSize, workerCount, bufferSize, chainID, externalRpc, handler)

		if err == nil {
			return nil
		}

		// Check for non-retryable gRPC errors
		if dgrpcError := dgrpc.AsGRPCError(err); dgrpcError != nil {
			switch dgrpcError.Code() {
			case codes.Unauthenticated:
				return fmt.Errorf("stream failure: %w", err)
			case codes.InvalidArgument:
				return fmt.Errorf("stream invalid: %w", err)
			}
		}

		// Check if we made progress - reset backoff when progress is made
		if *startBlock > prev {
			fmt.Printf("Progress detected (%d → %d), resetting backoff\n", prev, *startBlock)
			backOff.Reset()
			continue
		}

		// Get next backoff delay
		sleepFor := backOff.NextBackOff()
		if sleepFor == backoff.Stop {
			return fmt.Errorf("backoff expired: %w", err)
		}
		time.Sleep(sleepFor)
	}
}

// Type definitions for the pipeline
type seqResponse struct {
	seq  uint64
	resp *pbfirehose.Response
}

type seqBlock struct {
	seq   uint64
	block *types.Block
}

// streamReader reads from the Firehose stream and sends responses with sequence numbers
func streamReader(stream pbfirehose.Stream_BlocksClient, rawCh chan<- seqResponse, errCh chan<- error) {
	defer close(rawCh)
	var seq uint64
	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				fmt.Printf("Stream completed successfully after %d messages\n", seq)
				break
			}
			errCh <- fmt.Errorf("error receiving from stream (seq %d): %w", seq, err)
			return
		}
		rawCh <- seqResponse{seq: seq, resp: resp}
		seq++
	}
}

// blockConverter converts Firehose blocks to Geth blocks using a worker pool
func blockConverter(
	rawCh <-chan seqResponse,
	blockCh chan<- seqBlock,
	workerCount int,
	chainID *big.Int,
	externalRpc string,
) {
	var wg sync.WaitGroup
	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for sr := range rawCh {
				ethBlock := &pbeth.Block{}
				if err := sr.resp.Block.UnmarshalTo(ethBlock); err != nil {
					fmt.Printf("failed to unmarshal block (seq: %d): %v\n", sr.seq, err)
					continue
				}
				block, err := convertFirehoseBlockToGethBlock(ethBlock, chainID, externalRpc)
				if err != nil {
					fmt.Printf("failed to convert block %d: %v\n", ethBlock.Number, err)
					continue
				}
				blockCh <- seqBlock{seq: sr.seq, block: block}
			}
		}()
	}

	// Close blockCh when all workers are done
	go func() {
		wg.Wait()
		close(blockCh)
	}()
}

// blockBatcher processes blocks in order and batches them for the handler
func blockBatcher(
	blockCh <-chan seqBlock,
	batchSize int,
	startBlock *int,
	handler func(blocks []*types.Block, batchNum int) error,
	errCh chan<- error,
	doneCh chan<- struct{},
) {
	var (
		blocks   []*types.Block
		batchNum int
		nextSeq  uint64 = 0
		buffer          = make(map[uint64]*types.Block)
	)

	for sb := range blockCh {
		buffer[sb.seq] = sb.block
		// Drain in-order blocks from buffer
		for {
			block, ok := buffer[nextSeq]
			if !ok {
				break
			}
			blocks = append(blocks, block)
			delete(buffer, nextSeq)
			nextSeq++
			if len(blocks) >= batchSize {
				if err := handler(blocks, batchNum); err != nil {
					errCh <- fmt.Errorf("handler failed for batch %d (blocks %d-%d): %w",
						batchNum, blocks[0].NumberU64(), blocks[len(blocks)-1].NumberU64(), err)
					return
				}
				*startBlock = int(blocks[len(blocks)-1].NumberU64() + 1)
				batchNum++
				blocks = blocks[:0]
			}
		}
	}

	// Process any remaining blocks
	if len(blocks) > 0 {
		if err := handler(blocks, batchNum); err != nil {
			errCh <- fmt.Errorf("handler failed for final batch %d (blocks %d-%d): %w",
				batchNum, blocks[0].NumberU64(), blocks[len(blocks)-1].NumberU64(), err)
			return
		}
		*startBlock = int(blocks[len(blocks)-1].NumberU64() + 1)
	}

	fmt.Printf("Block batching completed. Total batches: %d, final block: %d\n", batchNum+1, *startBlock-1)
	close(doneCh)
}

func processFirehoseBlocksWithClient(
	client pbfirehose.StreamClient,
	grpcOpts []grpc.CallOption,
	startBlock *int,
	endBlock uint64,
	batchSize int,
	workerCount int,
	bufferSize int,
	chainID *big.Int,
	externalRpc string,
	handler func(blocks []*types.Block, batchNum int) error,
) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.Blocks(ctx, &pbfirehose.Request{
		StartBlockNum: int64(*startBlock),
		StopBlockNum:  endBlock,
	}, grpcOpts...)
	if err != nil {
		return fmt.Errorf("failed to start block stream: %w", err)
	}

	// Create channels for the pipeline
	rawCh := make(chan seqResponse, bufferSize)
	blockCh := make(chan seqBlock, bufferSize)
	errCh := make(chan error, 1)
	doneCh := make(chan struct{})

	// Start the pipeline stages
	go streamReader(stream, rawCh, errCh)
	go blockConverter(rawCh, blockCh, workerCount, chainID, externalRpc)
	go blockBatcher(blockCh, batchSize, startBlock, handler, errCh, doneCh)

	// Wait for completion or error
	select {
	case err := <-errCh:
		return err
	case <-doneCh:
		return nil
	}
}
