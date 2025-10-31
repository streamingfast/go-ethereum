package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func (s *hookedStateDB) GetLogs(txHash common.Hash, blockNumber uint64, blockHash common.Hash, blockTime uint64) []*types.Log {
	return s.inner.GetLogs(txHash, blockNumber, blockHash, blockTime)
}
