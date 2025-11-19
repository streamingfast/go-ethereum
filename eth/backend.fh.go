package eth

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node/flashblock"
)

func (n *Ethereum) initFlashblockController() error {
	if !n.config.FlashblocksEnabled {
		return nil
	}

	client := flashblock.NewClient(n.config.FlashblocksWSURL, nil, log.New("component", "flashblock/client"))
	controller := flashblock.NewController(n.blockchain, client, log.New("component", "flashblock"))

	n.flashblockCtrl = controller
	return nil
}

func (n *Ethereum) startFlashblockController() error {
	if n.flashblockCtrl == nil {
		return nil
	}

	return n.flashblockCtrl.Start()
}

func (n *Ethereum) stopFlashblockController() error {
	if n.flashblockCtrl == nil {
		return nil
	}

	return n.flashblockCtrl.Stop()
}
