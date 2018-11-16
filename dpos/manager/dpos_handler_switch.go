package manager

import (
	"bytes"
	"time"

	"github.com/elastos/Elastos.ELA/blockchain"
	"github.com/elastos/Elastos.ELA/core"
	"github.com/elastos/Elastos.ELA/dpos/log"
	msg2 "github.com/elastos/Elastos.ELA/dpos/p2p/msg"

	"github.com/elastos/Elastos.ELA.Utility/common"
)

type DposEventConditionHandler interface {
	TryStartNewConsensus(b *core.Block) bool

	ChangeView(firstBlockHash *common.Uint256)

	StartNewProposal(p core.DPosProposal)

	ProcessAcceptVote(p core.DPosProposalVote)
	ProcessRejectVote(p core.DPosProposalVote)
}

type DposHandlerSwitch interface {
	ViewListener
	DposEventConditionHandler

	Initialize(dispatcher ProposalDispatcher, consensus Consensus)
	SwitchTo(isOnDuty bool)

	FinishConsensus()

	ProcessPing(id common.Uint256, height uint32)
	ProcessPong(id common.Uint256, height uint32)

	RequestAbnormalRecovering()
	HelpToRecoverAbnormal(id common.Uint256, height uint32)
	RecoverAbnormal(status *msg2.ConsensusStatus)

	ResponseGetBlocks(id common.Uint256, startBlockHeight, endBlockHeight uint32)
}

type dposHandlerSwitch struct {
	proposalDispatcher ProposalDispatcher
	consensus          Consensus
	network            DposNetwork
	manager            DposManager

	onDutyHandler  *DposOnDutyHandler
	normalHandler  *DposNormalHandler
	currentHandler DposEventConditionHandler

	eventMonitor *log.EventMonitor

	isAbnormal bool
}

func NewHandler(network DposNetwork, manager DposManager, monitor *log.EventMonitor) DposHandlerSwitch {
	h := &dposHandlerSwitch{
		network:      network,
		manager:      manager,
		eventMonitor: monitor,
		isAbnormal:   false,
	}

	h.normalHandler = &DposNormalHandler{h}
	h.onDutyHandler = &DposOnDutyHandler{h}

	h.SwitchTo(false)

	return h
}

func (h *dposHandlerSwitch) Initialize(dispatcher ProposalDispatcher, consensus Consensus) {
	h.proposalDispatcher = dispatcher
	h.consensus = consensus
}

func (h *dposHandlerSwitch) AddListeners(listeners ...log.EventListener) {
	for _, l := range listeners {
		h.eventMonitor.RegisterListener(l)
	}
}

func (h *dposHandlerSwitch) SwitchTo(onDuty bool) {
	if onDuty {
		h.currentHandler = h.onDutyHandler
	} else {
		h.currentHandler = h.normalHandler
	}
	h.consensus.SetOnDuty(true)
}

func (h *dposHandlerSwitch) FinishConsensus() {
	h.proposalDispatcher.FinishConsensus()
}

func (h *dposHandlerSwitch) StartNewProposal(p core.DPosProposal) {
	h.currentHandler.StartNewProposal(p)

	rawData := new(bytes.Buffer)
	p.Serialize(rawData)
	proposalEvent := log.ProposalEvent{
		Proposal:     p.Sponsor,
		BlockHash:    p.BlockHash,
		ReceivedTime: time.Now(),
		RawData:      rawData.Bytes(),
		Result:       false,
	}
	h.eventMonitor.OnProposalArrived(proposalEvent)
}

func (h *dposHandlerSwitch) ChangeView(firstBlockHash *common.Uint256) {
	h.currentHandler.ChangeView(firstBlockHash)

	viewEvent := log.ViewEvent{
		OnDutyArbitrator: h.consensus.GetOnDutyArbitrator(),
		StartTime:        time.Now(),
		Offset:           h.consensus.GetViewOffset(),
		Height:           h.proposalDispatcher.CurrentHeight(),
	}
	h.eventMonitor.OnViewStarted(viewEvent)
}

func (h *dposHandlerSwitch) TryStartNewConsensus(b *core.Block) bool {
	if _, ok := h.manager.GetBlockCache().TryGetValue(b.Hash()); ok {
		log.Info("[TryStartNewConsensus] failed, already have the block")
		return false
	}

	if h.proposalDispatcher.IsVoteSlotEmpty() {
		if h.currentHandler.TryStartNewConsensus(b) {
			rawData := new(bytes.Buffer)
			b.Serialize(rawData)
			c := log.ConsensusEvent{StartTime: time.Now(), Height: b.Height, RawData: rawData.Bytes()}
			h.eventMonitor.OnConsensusStarted(c)
			return true
		}
	}

	//todo record block into database
	return false
}

func (h *dposHandlerSwitch) ProcessAcceptVote(p core.DPosProposalVote) {
	h.currentHandler.ProcessAcceptVote(p)

	rawData := new(bytes.Buffer)
	p.Serialize(rawData)
	voteEvent := log.VoteEvent{Signer: p.Signer, ReceivedTime: time.Now(), Result: true, RawData: rawData.Bytes()}
	h.eventMonitor.OnVoteArrived(voteEvent)
}

func (h *dposHandlerSwitch) ProcessRejectVote(p core.DPosProposalVote) {
	h.currentHandler.ProcessRejectVote(p)

	rawData := new(bytes.Buffer)
	p.Serialize(rawData)
	voteEvent := log.VoteEvent{Signer: p.Signer, ReceivedTime: time.Now(), Result: false, RawData: rawData.Bytes()}
	h.eventMonitor.OnVoteArrived(voteEvent)
}

func (h *dposHandlerSwitch) ResponseGetBlocks(id common.Uint256, startBlockHeight, endBlockHeight uint32) {
	//todo limit max height range (endBlockHeight - startBlockHeight)
	currentHeight := h.proposalDispatcher.CurrentHeight()

	endHeight := endBlockHeight
	if currentHeight < endBlockHeight {
		endHeight = currentHeight
	}
	blocks, blockConfirms, err := blockchain.DefaultLedger.GetBlocksAndConfirms(startBlockHeight, endHeight)
	if err != nil {
		log.Error(err)
		return
	}

	if currentBlock := h.proposalDispatcher.GetProcessingBlock(); currentBlock != nil {
		blocks = append(blocks, currentBlock)
	}

	msg := &msg2.ResponseBlocksMessage{Command: msg2.ResponseBlocks, Blocks: blocks, BlockConfirms: blockConfirms}
	h.network.SendMessageToPeer(id, msg)
}

func (h *dposHandlerSwitch) ProcessPing(id common.Uint256, height uint32) {
	h.processHeartBeat(id, height)
}

func (h *dposHandlerSwitch) ProcessPong(id common.Uint256, height uint32) {
	h.processHeartBeat(id, height)
}

func (h *dposHandlerSwitch) RequestAbnormalRecovering() {
	h.proposalDispatcher.RequestAbnormalRecovering()
	h.isAbnormal = true
}

func (h *dposHandlerSwitch) HelpToRecoverAbnormal(id common.Uint256, height uint32) {
	status := &msg2.ConsensusStatus{}

	var err error
	if status.MissingBlocks, status.MissingBlockConfirms, err = blockchain.DefaultLedger.GetBlocksAndConfirms(height, 0); err != nil {
		log.Error("Error occurred when collect consensus status from leger: ", err)
		return
	}

	if err := h.consensus.CollectConsensusStatus(height, status); err != nil {
		log.Error("Error occurred when collect consensus status from consensus object: ", err)
		return
	}

	if err := h.proposalDispatcher.CollectConsensusStatus(height, status); err != nil {
		log.Error("Error occurred when collect consensus status from proposal dispatcher object: ", err)
		return
	}

	msg := &msg2.ResponseConsensusMessage{Consensus: *status}
	h.network.SendMessageToPeer(id, msg)
}

func (h *dposHandlerSwitch) RecoverAbnormal(status *msg2.ConsensusStatus) {

	if err := blockchain.DefaultLedger.AppendBlocksAndConfirms(status.MissingBlocks, status.MissingBlockConfirms); err != nil {
		log.Error("Error occurred when recover leger: ", err)
		return
	}

	if err := h.proposalDispatcher.RecoverFromConsensusStatus(status); err != nil {
		log.Error("Error occurred when recover proposal dispatcher object: ", err)
		return
	}

	if err := h.consensus.RecoverFromConsensusStatus(status); err != nil {
		log.Error("Error occurred when recover consensus object: ", err)
		return
	}

	h.isAbnormal = false
}

func (h *dposHandlerSwitch) OnViewChanged(isOnDuty bool) {
	h.SwitchTo(isOnDuty)

	firstBlockHash, ok := h.manager.GetBlockCache().GetFirstArrivedBlockHash()
	if isOnDuty && !ok {
		log.Warn("[OnViewChanged] firstBlockHash is nil")
		return
	}
	log.Info("OnViewChanged, onduty, getBlock from first block hash:", firstBlockHash)
	h.ChangeView(&firstBlockHash)
}

func (h *dposHandlerSwitch) processHeartBeat(id common.Uint256, height uint32) {
	if h.tryRequestBlocks(id, height) {
		log.Info("Found higher block, requesting it.")
	}
}

func (h *dposHandlerSwitch) tryRequestBlocks(id common.Uint256, sourceHeight uint32) bool {
	height := h.proposalDispatcher.CurrentHeight()
	if sourceHeight > height {
		msg := &msg2.GetBlocksMessage{
			StartBlockHeight: height,
			EndBlockHeight:   sourceHeight}
		h.network.SendMessageToPeer(id, msg)

		return true
	}
	return false
}
