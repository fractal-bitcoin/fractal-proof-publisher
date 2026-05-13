package txbuilder

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

const (
	DefaultRevealPostage    int64 = 546
	DefaultSendChangeMin    int64 = 546
	defaultCommitOverhead   int64 = 11
	defaultP2WPKHInputVSize int64 = 68
	defaultP2TRInputVSize   int64 = 58
)

type FundingPlan struct {
	SelectedInputs    []model.UTXO
	TotalInputValue   int64
	EstimatedVBytes   int64
	FeeValue          int64
	CommitOutputValue int64
	ChangeValue       int64
}

type BuildInput struct {
	Inputs            []model.UTXO
	ChangeAddress     string
	Network           string
	CommitPlan        inscription.CommitPlan
	FeeRateSatVB      int64
	CommitOutputValue int64
	ChangeValue       int64
	RevealOutputValue int64
	RevealRecipient   string
}

type RevealPlan struct {
	InputValue            int64
	FeeValue              int64
	RecipientValue        int64
	RecipientAddress      string
	RecipientScript       []byte
	WitnessProgram        []byte
	ScriptSpend           inscription.ScriptSpendPlan
	ControlBlock          []byte
	LeafHash              []byte
	MerkleRoot            []byte
	CommitOutPoint        wire.OutPoint
	CommitPrevOutput      *wire.TxOut
	CommitTxID            string
	TxIn                  *wire.TxIn
	TxOut                 *wire.TxOut
	Tx                    *wire.MsgTx
	RawTxHex              string
	WitnessStack          wire.TxWitness
	EstimatedWitnessBytes int64
	EstimatedWitnessVB    int64
	EstimatedVBytes       int64
}

type BuildResult struct {
	RawTxHex           string
	EstimatedVBytes    int64
	FeeValue           int64
	ChangeValue        int64
	SelectedInputs     []model.UTXO
	Tx                 *wire.MsgTx
	Network            string
	CommitPlan         inscription.CommitPlan
	CommitAddress      string
	CommitOutputScript []byte
	CommitOutputValue  int64
	RevealScript       []byte
	Reveal             RevealPlan
}

func PlanFunding(utxos []model.UTXO, feeRateSatVB, commitOutputValue, sendChangeMinValue int64) (FundingPlan, error) {
	if commitOutputValue <= 0 {
		commitOutputValue = DefaultRevealPostage
	}
	if sendChangeMinValue <= 0 {
		sendChangeMinValue = DefaultSendChangeMin
	}
	if feeRateSatVB <= 0 {
		feeRateSatVB = 1
	}

	selected, total, err := SelectInputs(utxos, commitOutputValue)
	if err != nil {
		return FundingPlan{}, err
	}

	for {
		feeWithoutChange := estimateCommitFee(selected, feeRateSatVB, false)
		changeWithoutChangeOutput := total - commitOutputValue - feeWithoutChange
		if changeWithoutChangeOutput < 0 {
			var next model.UTXO
			if len(utxos) <= len(selected) {
				return FundingPlan{}, fmt.Errorf("insufficient funds: need %d", commitOutputValue+feeWithoutChange)
			}
			next = utxos[len(selected)]
			selected = append(selected, next)
			total += next.AmountSat
			continue
		}

		hasChange := changeWithoutChangeOutput >= sendChangeMinValue
		feeValue := total - commitOutputValue
		changeValue := int64(0)
		if hasChange {
			feeValue = estimateCommitFee(selected, feeRateSatVB, true)
			changeValue = total - commitOutputValue - feeValue
			if changeValue < 0 {
				if len(utxos) <= len(selected) {
					return FundingPlan{}, fmt.Errorf("insufficient funds: need %d", commitOutputValue+feeValue)
				}
				next := utxos[len(selected)]
				selected = append(selected, next)
				total += next.AmountSat
				continue
			}
			if changeValue < sendChangeMinValue {
				feeValue = total - commitOutputValue
				changeValue = 0
				hasChange = false
			}
		}

		return FundingPlan{
			SelectedInputs:    selected,
			TotalInputValue:   total,
			EstimatedVBytes:   estimateCommitVBytes(selected, hasChange),
			FeeValue:          feeValue,
			CommitOutputValue: commitOutputValue,
			ChangeValue:       changeValue,
		}, nil
	}
}

func estimateCommitFee(inputs []model.UTXO, feeRateSatVB int64, hasChange bool) int64 {
	return estimateCommitVBytes(inputs, hasChange) * feeRateSatVB
}

func estimateCommitVBytes(inputs []model.UTXO, hasChange bool) int64 {
	vbytes := defaultCommitOverhead + estimateInputVBytes(inputs) + 43
	if hasChange {
		vbytes += 43
	}
	return vbytes
}

func estimateInputVBytes(inputs []model.UTXO) int64 {
	var total int64
	for _, in := range inputs {
		switch in.AddressType {
		case "p2tr":
			total += defaultP2TRInputVSize
		default:
			total += defaultP2WPKHInputVSize
		}
	}
	return total
}

func estimateRevealFee(vbytes, feeRateSatVB int64) int64 {
	if vbytes <= 0 {
		return 0
	}
	if feeRateSatVB <= 0 {
		feeRateSatVB = 1
	}
	return vbytes * feeRateSatVB
}

func EstimateRevealFee(commitPlan inscription.CommitPlan, network, recipientAddress string, feeRateSatVB int64) (int64, int64, error) {
	if commitPlan.InternalKey == nil {
		return 0, 0, fmt.Errorf("inscription commit plan internal key is required")
	}
	if len(commitPlan.Reveal.Payload.Body) == 0 {
		return 0, 0, fmt.Errorf("inscription commit plan is required")
	}
	params := ParamsForNetwork(network)
	spendPlan, err := commitPlan.ScriptSpendPlan()
	if err != nil {
		return 0, 0, err
	}
	commitOutputScript, err := commitPlan.OutputScript(params)
	if err != nil {
		return 0, 0, err
	}
	revealInput := BuildInput{
		ChangeAddress:     recipientAddress,
		Network:           network,
		CommitPlan:        commitPlan,
		FeeRateSatVB:      feeRateSatVB,
		CommitOutputValue: DefaultRevealPostage,
		RevealOutputValue: DefaultRevealPostage,
		RevealRecipient:   recipientAddress,
	}
	revealPlan, err := buildRevealPlan(revealInput, spendPlan, commitOutputScript)
	if err != nil {
		return 0, 0, err
	}
	feeValue := estimateRevealFee(revealPlan.EstimatedVBytes, feeRateSatVB)
	return revealPlan.EstimatedVBytes, feeValue, nil
}

func cloneRevealPlan(plan RevealPlan) RevealPlan {
	cloned := plan
	cloned.RecipientScript = append([]byte(nil), plan.RecipientScript...)
	cloned.WitnessProgram = append([]byte(nil), plan.WitnessProgram...)
	cloned.ControlBlock = append([]byte(nil), plan.ControlBlock...)
	cloned.LeafHash = append([]byte(nil), plan.LeafHash...)
	cloned.MerkleRoot = append([]byte(nil), plan.MerkleRoot...)
	cloned.WitnessStack = append(wire.TxWitness(nil), plan.WitnessStack...)
	if plan.CommitPrevOutput != nil {
		cloned.CommitPrevOutput = &wire.TxOut{
			Value:    plan.CommitPrevOutput.Value,
			PkScript: append([]byte(nil), plan.CommitPrevOutput.PkScript...),
		}
	}
	if plan.TxIn != nil {
		txIn := wire.NewTxIn(&plan.TxIn.PreviousOutPoint, nil, nil)
		txIn.Sequence = plan.TxIn.Sequence
		txIn.Witness = append(wire.TxWitness(nil), plan.TxIn.Witness...)
		cloned.TxIn = txIn
	}
	if plan.TxOut != nil {
		cloned.TxOut = &wire.TxOut{Value: plan.TxOut.Value, PkScript: append([]byte(nil), plan.TxOut.PkScript...)}
	}
	if plan.Tx != nil {
		clonedTx := plan.Tx.Copy()
		cloned.Tx = clonedTx
		if len(clonedTx.TxIn) > 0 {
			cloned.TxIn = clonedTx.TxIn[0]
		}
		if len(clonedTx.TxOut) > 0 {
			cloned.TxOut = clonedTx.TxOut[0]
		}
	}
	return cloned
}

func FinalizeRevealPlan(plan RevealPlan, commitTx *wire.MsgTx) (RevealPlan, error) {
	if commitTx == nil {
		return RevealPlan{}, fmt.Errorf("commit tx is required")
	}
	if len(commitTx.TxOut) == 0 {
		return RevealPlan{}, fmt.Errorf("commit tx has no outputs")
	}
	if plan.CommitPrevOutput == nil {
		return RevealPlan{}, fmt.Errorf("commit prev output is required")
	}
	commitOutput := commitTx.TxOut[0]
	if commitOutput.Value != plan.CommitPrevOutput.Value {
		return RevealPlan{}, fmt.Errorf("commit output value mismatch: %d != %d", commitOutput.Value, plan.CommitPrevOutput.Value)
	}
	if !bytes.Equal(commitOutput.PkScript, plan.CommitPrevOutput.PkScript) {
		return RevealPlan{}, fmt.Errorf("commit output script mismatch")
	}

	finalized := cloneRevealPlan(plan)
	commitHash := commitTx.TxHash()
	finalized.CommitTxID = commitHash.String()
	finalized.CommitOutPoint = wire.OutPoint{Hash: commitHash, Index: 0}
	if finalized.Tx == nil {
		return RevealPlan{}, fmt.Errorf("reveal tx is required")
	}
	if len(finalized.Tx.TxIn) == 0 {
		return RevealPlan{}, fmt.Errorf("reveal tx has no inputs")
	}
	finalized.Tx.TxIn[0].PreviousOutPoint = finalized.CommitOutPoint
	finalized.TxIn = finalized.Tx.TxIn[0]

	var buf bytes.Buffer
	if err := finalized.Tx.Serialize(&buf); err != nil {
		return RevealPlan{}, fmt.Errorf("serialize finalized reveal tx: %w", err)
	}
	finalized.RawTxHex = hex.EncodeToString(buf.Bytes())
	finalized.EstimatedVBytes = int64(finalized.Tx.SerializeSize())
	return finalized, nil
}

func FinalizeRevealFromCommitHex(plan RevealPlan, signedCommitHex string) (RevealPlan, error) {
	rawCommitTx, err := hex.DecodeString(signedCommitHex)
	if err != nil {
		return RevealPlan{}, fmt.Errorf("decode signed commit tx: %w", err)
	}
	var commitTx wire.MsgTx
	if err := commitTx.Deserialize(bytes.NewReader(rawCommitTx)); err != nil {
		return RevealPlan{}, fmt.Errorf("deserialize signed commit tx: %w", err)
	}
	return FinalizeRevealPlan(plan, &commitTx)
}

func SignRevealPlan(plan RevealPlan, keyMaterial keys.KeyMaterial) (RevealPlan, error) {
	if keyMaterial.PrivateKey == nil {
		return RevealPlan{}, fmt.Errorf("private key is required")
	}
	if plan.Tx == nil {
		return RevealPlan{}, fmt.Errorf("reveal tx is required")
	}
	if plan.CommitPrevOutput == nil {
		return RevealPlan{}, fmt.Errorf("commit prev output is required")
	}
	if len(plan.Tx.TxIn) == 0 {
		return RevealPlan{}, fmt.Errorf("reveal tx has no inputs")
	}

	signed := cloneRevealPlan(plan)
	prevFetcher := txscript.NewCannedPrevOutputFetcher(plan.CommitPrevOutput.PkScript, plan.CommitPrevOutput.Value)
	sigHashes := txscript.NewTxSigHashes(signed.Tx, prevFetcher)
	sig, err := txscript.RawTxInTapscriptSignature(
		signed.Tx,
		sigHashes,
		0,
		plan.CommitPrevOutput.Value,
		plan.CommitPrevOutput.PkScript,
		plan.ScriptSpend.TapLeaf,
		txscript.SigHashDefault,
		keyMaterial.PrivateKey,
	)
	if err != nil {
		return RevealPlan{}, fmt.Errorf("tapscript reveal signature: %w", err)
	}
	witnessStack, err := plan.ScriptSpend.WitnessStack(sig)
	if err != nil {
		return RevealPlan{}, err
	}
	signed.Tx.TxIn[0].Witness = append(wire.TxWitness(nil), witnessStack...)
	signed.TxIn = signed.Tx.TxIn[0]
	signed.WitnessStack = append(wire.TxWitness(nil), witnessStack...)

	var buf bytes.Buffer
	if err := signed.Tx.Serialize(&buf); err != nil {
		return RevealPlan{}, fmt.Errorf("serialize signed reveal tx: %w", err)
	}
	signed.RawTxHex = hex.EncodeToString(buf.Bytes())
	signed.EstimatedVBytes = int64(signed.Tx.SerializeSize())
	return signed, nil
}

func Build(input BuildInput) (BuildResult, error) {
	if len(input.Inputs) == 0 {
		return BuildResult{}, fmt.Errorf("at least one input is required")
	}
	if input.ChangeAddress == "" {
		return BuildResult{}, fmt.Errorf("change address is required")
	}
	if input.CommitPlan.InternalKey == nil {
		return BuildResult{}, fmt.Errorf("inscription commit plan internal key is required")
	}
	if len(input.CommitPlan.Reveal.Payload.Body) == 0 {
		return BuildResult{}, fmt.Errorf("inscription commit plan is required")
	}
	if input.CommitOutputValue <= 0 {
		input.CommitOutputValue = DefaultRevealPostage
	}

	params := ParamsForNetwork(input.Network)
	spendPlan, err := input.CommitPlan.ScriptSpendPlan()
	if err != nil {
		return BuildResult{}, err
	}
	commitOutputScript, err := input.CommitPlan.OutputScript(params)
	if err != nil {
		return BuildResult{}, err
	}
	commitAddress, err := input.CommitPlan.Address(params)
	if err != nil {
		return BuildResult{}, err
	}
	revealPlan, err := buildRevealPlan(input, spendPlan, commitOutputScript)
	if err != nil {
		return BuildResult{}, err
	}

	tx := wire.NewMsgTx(wire.TxVersion)
	for _, in := range input.Inputs {
		h, err := chainhash.NewHashFromStr(in.TxID)
		if err != nil {
			return BuildResult{}, fmt.Errorf("parse input txid %s: %w", in.TxID, err)
		}
		outPoint := wire.NewOutPoint(h, in.Vout)
		txIn := wire.NewTxIn(outPoint, nil, nil)
		tx.AddTxIn(txIn)
	}

	changeAddr, err := decodeAddress(input.ChangeAddress, input.Network)
	if err != nil {
		return BuildResult{}, err
	}
	changeScript, err := txscript.PayToAddrScript(changeAddr)
	if err != nil {
		return BuildResult{}, fmt.Errorf("build change script: %w", err)
	}

	tx.AddTxOut(wire.NewTxOut(input.CommitOutputValue, commitOutputScript))
	if input.ChangeValue > 0 {
		tx.AddTxOut(wire.NewTxOut(input.ChangeValue, changeScript))
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return BuildResult{}, fmt.Errorf("serialize tx: %w", err)
	}
	rawTxHex := hex.EncodeToString(buf.Bytes())

	return BuildResult{
		RawTxHex:           rawTxHex,
		EstimatedVBytes:    int64(tx.SerializeSize()),
		FeeValue:           0,
		ChangeValue:        input.ChangeValue,
		SelectedInputs:     input.Inputs,
		Tx:                 tx,
		Network:            input.Network,
		CommitPlan:         input.CommitPlan,
		CommitAddress:      commitAddress,
		CommitOutputScript: commitOutputScript,
		CommitOutputValue:  input.CommitOutputValue,
		RevealScript:       append([]byte(nil), spendPlan.RevealScript...),
		Reveal:             revealPlan,
	}, nil
}

func buildRevealPlan(input BuildInput, spendPlan inscription.ScriptSpendPlan, commitOutputScript []byte) (RevealPlan, error) {
	controlBlock, err := spendPlan.ControlBlockBytes()
	if err != nil {
		return RevealPlan{}, err
	}
	witnessProgram, err := spendPlan.WitnessProgram()
	if err != nil {
		return RevealPlan{}, err
	}
	recipientAddress := input.RevealRecipient
	if recipientAddress == "" {
		recipientAddress = input.ChangeAddress
	}
	recipientAddr, err := decodeAddress(recipientAddress, input.Network)
	if err != nil {
		return RevealPlan{}, err
	}
	recipientScript, err := txscript.PayToAddrScript(recipientAddr)
	if err != nil {
		return RevealPlan{}, fmt.Errorf("build reveal recipient script: %w", err)
	}
	recipientValue := input.RevealOutputValue
	if recipientValue <= 0 {
		recipientValue = input.CommitOutputValue
	}
	if recipientValue > input.CommitOutputValue {
		return RevealPlan{}, fmt.Errorf("reveal output value %d exceeds commit output value %d", recipientValue, input.CommitOutputValue)
	}
	witnessStack, err := spendPlan.WitnessStack()
	if err != nil {
		return RevealPlan{}, err
	}
	estimatedWitnessBytes, err := spendPlan.EstimatedWitnessSize()
	if err != nil {
		return RevealPlan{}, err
	}

	commitOutPoint := wire.OutPoint{Hash: chainhash.Hash{}, Index: 0}
	commitPrevOutput := wire.NewTxOut(input.CommitOutputValue, append([]byte(nil), commitOutputScript...))
	txIn := wire.NewTxIn(&commitOutPoint, nil, nil)
	txIn.Sequence = wire.MaxTxInSequenceNum
	txIn.Witness = append(wire.TxWitness(nil), witnessStack...)
	txOut := wire.NewTxOut(recipientValue, append([]byte(nil), recipientScript...))
	revealTx := wire.NewMsgTx(wire.TxVersion)
	revealTx.AddTxIn(txIn)
	revealTx.AddTxOut(txOut)

	var buf bytes.Buffer
	if err := revealTx.Serialize(&buf); err != nil {
		return RevealPlan{}, fmt.Errorf("serialize reveal tx: %w", err)
	}

	return RevealPlan{
		InputValue:            input.CommitOutputValue,
		RecipientValue:        recipientValue,
		FeeValue:              input.CommitOutputValue - recipientValue,
		RecipientAddress:      recipientAddress,
		RecipientScript:       append([]byte(nil), recipientScript...),
		WitnessProgram:        append([]byte(nil), witnessProgram...),
		ScriptSpend:           spendPlan,
		ControlBlock:          controlBlock,
		LeafHash:              append([]byte(nil), spendPlan.LeafHash...),
		MerkleRoot:            append([]byte(nil), spendPlan.MerkleRoot...),
		CommitOutPoint:        commitOutPoint,
		CommitPrevOutput:      commitPrevOutput,
		TxIn:                  txIn,
		TxOut:                 txOut,
		Tx:                    revealTx,
		RawTxHex:              hex.EncodeToString(buf.Bytes()),
		WitnessStack:          witnessStack,
		EstimatedWitnessBytes: estimatedWitnessBytes,
		EstimatedWitnessVB:    estimatedWitnessBytes,
		EstimatedVBytes:       int64(revealTx.SerializeSize()),
	}, nil
}

func Sign(unsigned BuildResult, keyMaterial keys.KeyMaterial) (string, error) {
	if unsigned.Tx == nil {
		return "", fmt.Errorf("unsigned tx is required")
	}
	if keyMaterial.PrivateKey == nil {
		return "", fmt.Errorf("private key is required")
	}

	prevFetcher, err := PreparePrevOutputFetcher(unsigned.SelectedInputs)
	if err != nil {
		return "", err
	}
	sigHashes := txscript.NewTxSigHashes(unsigned.Tx, prevFetcher)

	for idx, in := range unsigned.SelectedInputs {
		switch in.AddressType {
		case "p2wpkh":
			if err := SignP2WPKH(unsigned, sigHashes, idx, in, keyMaterial); err != nil {
				return "", err
			}
		case "p2tr":
			if err := SignP2TR(unsigned, sigHashes, idx, in, keyMaterial); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("unsupported input address type: %s", in.AddressType)
		}
	}

	var buf bytes.Buffer
	if err := unsigned.Tx.Serialize(&buf); err != nil {
		return "", fmt.Errorf("serialize signed tx: %w", err)
	}
	return hex.EncodeToString(buf.Bytes()), nil
}
