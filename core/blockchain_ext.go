package core

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// Contains extensions used in blockchain.go file that we put here to make it easier
// to merge new changes from upstream with minimal diffs.

var enabledFinalizedHeaderMismatchLogging = os.Getenv("FIREHOSE_ETHEREUM_TRACER_LOG_FINALIZED_HEADER_MISMATCH") == "true"

func (bc *BlockChain) logFinalizedHeaderMismatch(prefix string, current *types.Header, against *types.Header) {
	if !enabledFinalizedHeaderMismatchLogging {
		return
	}

	engine, ok := bc.engine.(consensus.PoSA)
	if !ok {
		return
	}

	if against == nil {
		return
	}

	finalizedRelative := engine.GetFinalizedHeader(bc, against)
	if current == nil && finalizedRelative == nil {
		return
	}

	if current == nil && finalizedRelative != nil {
		log.Info(fmt.Sprintf("CurrentFinalBlock() is nil but GetFinalizedHeader(tracedBlock) is set in %s", prefix),
			"relative", (*headerView)(finalizedRelative),
			"relative_against", (*longHeaderView)(against),
		)
		return
	}

	if current != nil && finalizedRelative == nil {
		log.Info(fmt.Sprintf("CurrentFinalBlock() is set but GetFinalizedHeader(tracedBlock) is nil %s", prefix),
			"current", (*headerView)(current),
			"relative_against", (*longHeaderView)(against),
		)
		return
	}

	if finalizedRelative.Number != current.Number || finalizedRelative.Hash() != current.Hash() {
		log.Info(fmt.Sprintf("CurrentFinalBlock() and GetFinalizedHeader(tracedBlock) differs %s", prefix),
			"current", (*headerView)(current),
			"relative", (*headerView)(finalizedRelative),
			"relative_against", (*longHeaderView)(against),
		)
	}
}

type headerView types.Header

func (h *headerView) String() string {
	if h == nil {
		return "<nil>"
	}

	header := (*types.Header)(h)
	hash := header.Hash()

	return fmt.Sprintf("#%d 0x%X..%X", header.Number, hash[:4], hash[28:])
}

type longHeaderView types.Header

func (h *longHeaderView) String() string {
	if h == nil {
		return "<nil>"
	}

	header := (*types.Header)(h)
	hash := header.Hash()

	parentNumber := header.Number.Uint64() - 1

	return fmt.Sprintf("#%d 0x%X..%X (parent #%d 0x%X..%X)", header.Number, hash[:4], hash[28:], parentNumber, header.ParentHash[:4], header.ParentHash[28:])
}
