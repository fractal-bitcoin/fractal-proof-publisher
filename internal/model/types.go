package model

type MessageType string

const (
	MessageTypeRegister MessageType = "register"
	MessageTypeProve    MessageType = "prove"
)

type MessageStatus string

const (
	MessageStatusBuilding        MessageStatus = "building"
	MessageStatusCommitSigned    MessageStatus = "commit_signed"
	MessageStatusCommitSent      MessageStatus = "commit_sent"
	MessageStatusCommitConfirmed MessageStatus = "commit_confirmed"
	MessageStatusRevealSent      MessageStatus = "reveal_sent"
	MessageStatusDone            MessageStatus = "done"
	MessageStatusFailed          MessageStatus = "failed"
)

type BlockStatus string

const (
	BlockStatusSkipped        BlockStatus = "skipped"
	BlockStatusWaitingConfirm BlockStatus = "waiting_confirm"
	BlockStatusReady          BlockStatus = "ready"
	BlockStatusFailed         BlockStatus = "failed"
)

type UTXOStatus string

const (
	UTXOStatusAvailable      UTXOStatus = "available"
	UTXOStatusPending        UTXOStatus = "pending"
	UTXOStatusReserved       UTXOStatus = "reserved"
	UTXOStatusSpentPending   UTXOStatus = "spent_pending"
	UTXOStatusSpentConfirmed UTXOStatus = "spent_confirmed"
	UTXOStatusInvalid        UTXOStatus = "invalid"
)

type UTXOSource string

const (
	UTXOSourceConfig UTXOSource = "config"
	UTXOSourceChange UTXOSource = "change"
)

type RegisterData struct {
	IndexRatioBP   uint16
	RewardAddrType string
	RewardAddr     string
	Name           string
}

type ProveData struct {
	IndexerID   string
	ProveHeight uint64
	ProveHash   string
}

type UTXO struct {
	TxID          string
	Vout          uint32
	AmountSat     int64
	Address       string
	ScriptPubKey  string
	AddressType   string
	Status        UTXOStatus
	Source        UTXOSource
	SpentByTxID   string
	ConfirmHeight uint64
}
