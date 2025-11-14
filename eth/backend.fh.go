package eth

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node/flash"
)

func (n *Ethereum) initFlashblockController() error {
	client := flash.NewClient("ws://localhost:1114", nil, log.New("component", "flashblock/client"))
	controller := flash.NewController(n.blockchain, client, log.New("component", "flashblock"))

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
