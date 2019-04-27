package manager

import (
	"github.com/elastos/Elastos.ELA/blockchain"
	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/dpos/state"
)

const (
	// firstTimeoutFactor specified the factor first dynamic change
	// arbitrators in one consensus
	// (timeout will occurred in about 9 minutes)
	firstTimeoutFactor = uint32(3)

	// othersTimeoutFactor specified the factor after first dynamic change
	// arbitrators in one consensus
	// (timeout will occurred in about 1 hours)
	othersTimeoutFactor = uint32(20)
)

type ViewChangesCountDown struct {
	dispatcher  *ProposalDispatcher
	consensus   *Consensus
	arbitrators state.Arbitrators

	handledPayloadHashes map[common.Uint256]struct{}
	timeoutRefactor      uint32
}

func (c *ViewChangesCountDown) Reset() {
	c.handledPayloadHashes = make(map[common.Uint256]struct{})
	c.timeoutRefactor = firstTimeoutFactor
}

func (c *ViewChangesCountDown) SetEliminated(hash common.Uint256) bool {
	if _, ok := c.handledPayloadHashes[hash]; !ok {
		c.handledPayloadHashes[hash] = struct{}{}
		c.timeoutRefactor += othersTimeoutFactor
		return true
	}
	return false
}

func (c *ViewChangesCountDown) IsTimeOut() bool {
	if blockchain.DefaultLedger.Blockchain.GetHeight()+1 <
		c.dispatcher.cfg.ChainParams.PublicDPOSHeight || c.arbitrators.IsInactiveMode() {
		return false
	}

	return c.consensus.GetViewOffset() >=
		uint32(c.arbitrators.GetArbitersCount())*c.timeoutRefactor
}
