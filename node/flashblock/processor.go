package flashblock

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

// StateProcessor is a copy of [core.StateProcessor] adapted for flashblocks, e.g. that it's able to
// process flash block and apply only the transactions included in the flashblock.
type StateProcessor struct {
	config *params.ChainConfig // Chain configuration options
	chain  *core.HeaderChain   // Canonical header chain
	signer types.Signer

	// State accumulation
	statedb     *state.StateDB // State database to apply changes to, incrementally
	usedGas     *uint64        // Total gas used so far, incrementally
	allLogs     []*types.Log
	receipts    types.Receipts
	gp          *core.GasPool
	lastTxIndex *uint64
}

func NewStateProcessor(
	config *params.ChainConfig,
	chain *core.HeaderChain,
	stateDB *state.StateDB,
	blockNumber *big.Int,
	blockTime uint64,
	gasLimit uint64,
) *StateProcessor {
	return &StateProcessor{
		config:  config,
		chain:   chain,
		signer:  types.MakeSigner(config, blockNumber, blockTime),
		statedb: stateDB,
		usedGas: new(uint64),
		gp:      new(core.GasPool).AddGas(gasLimit),
	}
}

func (p *StateProcessor) Reset(gasLimit uint64) {
	p.lastTxIndex = nil
	p.usedGas = new(uint64)
	p.gp = new(core.GasPool).AddGas(gasLimit)
}

type txmsg struct {
	msg  *core.Message
	tx   *types.Transaction
	hash common.Hash
}

// Process processes the state changes according to the Ethereum rules by running but using an
// incremental approach for working with flashblocks. This code here needs to closely align with
// [core.StateProcessor.Process] to ensure correctness.
func (p *StateProcessor) Process(block *types.Block, firehoseTracer *tracers.Firehose, cfg vm.Config, isLastFlashBlock bool) (*core.ProcessResult, *common.Hash, *common.Hash, error) {
	var (
		header      = block.Header()
		blockHash   = block.Hash()
		blockNumber = block.Number()
	)

	isFirstExecution := p.lastTxIndex == nil

	// Apply pre-execution system calls.
	tracingStateDB := vm.StateDB(p.statedb)
	if hooks := cfg.Tracer; hooks != nil {
		tracingStateDB = state.NewHookedState(p.statedb, hooks)
	}

	context := core.NewEVMBlockContext(header, p.chain, nil, p.config, p.statedb)
	evm := vm.NewEVM(context, tracingStateDB, p.config, cfg)

	if isFirstExecution {
		// Mutate the block and state according to any hard-fork specs
		if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
			misc.ApplyDAOHardFork(tracingStateDB)
		}
		misc.EnsureCreate2Deployer(p.config, block.Time(), tracingStateDB)
		if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
			core.ProcessBeaconBlockRoot(*beaconRoot, evm)
		}
		if p.config.IsPrague(block.Number(), block.Time()) || p.config.IsVerkle(block.Number(), block.Time()) {
			core.ProcessParentBlockHash(block.ParentHash(), evm)
		}
	}

	allTransactions := block.Transactions()

	var idxDelta int
	transactions := allTransactions
	if !isFirstExecution {
		transactions = allTransactions[*p.lastTxIndex:]
		idxDelta = int(*p.lastTxIndex)
	}

	txmsgs := make([]txmsg, len(transactions))

	// Convert all transactions to messages in parallel with 10 workers max
	const maxWorkers = 10
	numWorkers := min(len(transactions), maxWorkers)

	var wg sync.WaitGroup
	errChan := make(chan error, len(transactions))

	// Worker function to convert transactions to messages
	worker := func(jobs <-chan int) {
		defer wg.Done()
		for i := range jobs {
			tx := transactions[i]
			msg, err := core.TransactionToMessage(tx, p.signer, header.BaseFee)
			if err != nil {
				errChan <- fmt.Errorf("could not convert tx %d [%v]: %w", i, tx.Hash().Hex(), err)
				return
			}
			txmsgs[i] = txmsg{
				msg:  msg,
				tx:   tx,
				hash: tx.Hash(),
			}
		}
	}

	// Create job channel and start workers
	jobs := make(chan int, len(transactions))
	for range numWorkers {
		wg.Add(1)
		go worker(jobs)
	}

	// Send jobs
	for i := range transactions {
		jobs <- i
	}
	close(jobs)

	// Wait for all workers to complete
	wg.Wait()

	// Check for errors
	close(errChan)
	if len(errChan) > 0 {
		return nil, nil, nil, <-errChan
	}

	// Process the individual transactions using prepared txmsgs
	for i, txmsg := range txmsgs {
		p.statedb.SetTxContext(txmsg.hash, i+idxDelta)
		receipt, err := core.ApplyTransactionWithEVM(txmsg.msg, p.gp, p.statedb, blockNumber, blockHash, context.Time, txmsg.tx, p.usedGas, evm)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, txmsg.hash.Hex(), err)
		}
		p.receipts = append(p.receipts, receipt)
		p.allLogs = append(p.allLogs, receipt.Logs...)
	}

	if isFirstExecution {
		p.lastTxIndex = new(uint64)
		*p.lastTxIndex = uint64(len(block.Transactions()))
	} else {
		*p.lastTxIndex += uint64(len(transactions))
	}

	isIsthmus := p.config.IsIsthmus(block.Time())

	if !isLastFlashBlock {
		firehoseTracer.SnapshotFlashBlockForNextIteration()

		return &core.ProcessResult{
			Receipts: p.receipts,
			Requests: nil,
			Logs:     p.allLogs,
			GasUsed:  *p.usedGas,
		}, nil, nil, nil
	}

	var requests [][]byte
	if p.config.IsPrague(block.Number(), block.Time()) && !isIsthmus {
		// EIP-6110
		if err := core.ParseDepositLogs(&requests, p.allLogs, p.config); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse deposit logs: %w", err)
		}

		// EIP-7002
		if err := core.ProcessWithdrawalQueue(&requests, evm); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to process withdrawal queue: %w", err)
		}
		// EIP-7251
		if err := core.ProcessConsolidationQueue(&requests, evm); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to process consolidation queue: %w", err)
		}
	}

	if isIsthmus {
		requests = [][]byte{}
	}

	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	p.chain.Engine().Finalize(p.chain, header, p.statedb, block.Body())
	hashroot, err := p.statedb.Commit(blockNumber.Uint64(), true, true)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to commit state: %w", err)
	}
	header.Root = hashroot

	newBlockHash := header.Hash()

	return &core.ProcessResult{
		Receipts: p.receipts,
		Requests: requests,
		Logs:     p.allLogs,
		GasUsed:  *p.usedGas,
	}, &hashroot, &newBlockHash, nil
}

// ValidateState validates the various changes that happen after a state transition,
// such as amount of used gas, the receipt roots and the state root itself.
//
// It is a direct copy of [core.BlockValidator.ValidateState] with some parts commented out
// (state root and withdrawals root checks) that we skip for now.
func (p *StateProcessor) ValidateState(block *types.Block, res *core.ProcessResult) error {
	if res == nil {
		return errors.New("nil ProcessResult value")
	}
	header := block.Header()
	if block.GasUsed() != res.GasUsed {
		return fmt.Errorf("invalid gas used (remote: %d local: %d)", block.GasUsed(), res.GasUsed)
	}
	// Validate the received block's bloom with the one derived from the generated receipts.
	// For valid blocks this should always validate to true.
	//
	// Receipts must go through MakeReceipt to calculate the receipt's bloom
	// already. Merge the receipt's bloom together instead of recalculating
	// everything.
	rbloom := types.MergeBloom(res.Receipts)
	if rbloom != header.Bloom {
		return fmt.Errorf("invalid bloom (remote: %x  local: %x)", header.Bloom, rbloom)
	}

	// The receipt Trie's root (R = (Tr [[H1, R1], ... [Hn, Rn]]))
	receiptSha := types.DeriveSha(res.Receipts, trie.NewStackTrie(nil))
	if receiptSha != header.ReceiptHash {
		return fmt.Errorf("invalid receipt root hash (remote: %x local: %x)", header.ReceiptHash, receiptSha)
	}
	// // Validate the parsed requests match the expected header value.
	// if header.RequestsHash != nil {
	// 	reqhash := types.CalcRequestsHash(res.Requests)
	// 	if reqhash != *header.RequestsHash {
	// 		return fmt.Errorf("invalid requests hash (remote: %x local: %x)", *header.RequestsHash, reqhash)
	// 	}
	// } else if res.Requests != nil {
	// 	return errors.New("block has requests before prague fork")
	// }

	// // Validate the state root against the received state root and throw
	// // an error if they don't match.
	// if root := p.statedb.IntermediateRoot(p.config.IsEIP158(header.Number)); header.Root != root {
	// 	return fmt.Errorf("invalid merkle root (remote: %x local: %x) dberr: %w", header.Root, root, p.statedb.Error())
	// }
	// if p.config.IsOptimismIsthmus(block.Time()) {
	// 	if header.WithdrawalsHash == nil {
	// 		return errors.New("expected withdrawals root in OP-Stack post-Isthmus block header")
	// 	}
	// 	// Validate the withdrawals root against the L2 withdrawals storage, similar to how the StateRoot is verified.
	// 	if root := p.statedb.GetStorageRoot(params.OptimismL2ToL1MessagePasser); *header.WithdrawalsHash != root {
	// 		return fmt.Errorf("invalid withdrawals hash (remote: %s local: %s) dberr: %w", *header.WithdrawalsHash, root, p.statedb.Error())
	// 	}
	// }

	return nil
}
