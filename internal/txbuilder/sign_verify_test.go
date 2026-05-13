package txbuilder

import (
	"bytes"
	"encoding/hex"
	"testing"

	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func TestBuildReturnsCommitArtifacts(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	plan := env.CommitPlan(keyMaterial.PublicKey)

	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        plan,
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	expectedCommitScript, err := plan.OutputScript(mainnetParams())
	if err != nil {
		t.Fatalf("CommitPlan.OutputScript() error = %v", err)
	}
	expectedCommitAddress, err := plan.Address(mainnetParams())
	if err != nil {
		t.Fatalf("CommitPlan.Address() error = %v", err)
	}
	expectedSpendPlan, err := plan.ScriptSpendPlan()
	if err != nil {
		t.Fatalf("ScriptSpendPlan() error = %v", err)
	}
	expectedControlBlock, err := expectedSpendPlan.ControlBlockBytes()
	if err != nil {
		t.Fatalf("ControlBlockBytes() error = %v", err)
	}
	expectedWitnessBytes, err := expectedSpendPlan.EstimatedWitnessSize()
	if err != nil {
		t.Fatalf("EstimatedWitnessSize() error = %v", err)
	}

	if unsigned.CommitAddress != expectedCommitAddress {
		t.Fatalf("CommitAddress = %q, want %q", unsigned.CommitAddress, expectedCommitAddress)
	}
	if !bytes.Equal(unsigned.CommitOutputScript, expectedCommitScript) {
		t.Fatal("CommitOutputScript does not match commit plan")
	}
	if !bytes.Equal(unsigned.Reveal.ScriptSpend.RevealScript, expectedSpendPlan.RevealScript) {
		t.Fatal("Reveal.ScriptSpend.RevealScript does not match commit plan")
	}
	if !bytes.Equal(unsigned.Reveal.ControlBlock, expectedControlBlock) {
		t.Fatal("Reveal.ControlBlock does not match commit plan")
	}
	expectedWitnessProgram, err := expectedSpendPlan.WitnessProgram()
	if err != nil {
		t.Fatalf("WitnessProgram() error = %v", err)
	}
	if !bytes.Equal(unsigned.Reveal.WitnessProgram, expectedWitnessProgram) {
		t.Fatal("Reveal.WitnessProgram does not match commit plan")
	}
	if len(unsigned.Reveal.WitnessStack) != 2 {
		t.Fatalf("Reveal.WitnessStack len = %d, want 2", len(unsigned.Reveal.WitnessStack))
	}
	if !bytes.Equal(unsigned.Reveal.LeafHash, expectedSpendPlan.LeafHash) {
		t.Fatal("Reveal.LeafHash does not match commit plan")
	}
	if !bytes.Equal(unsigned.Reveal.MerkleRoot, expectedSpendPlan.MerkleRoot) {
		t.Fatal("Reveal.MerkleRoot does not match commit plan")
	}
	if unsigned.Reveal.EstimatedWitnessBytes != expectedWitnessBytes {
		t.Fatalf("Reveal.EstimatedWitnessBytes = %d, want %d", unsigned.Reveal.EstimatedWitnessBytes, expectedWitnessBytes)
	}
	if len(unsigned.Tx.TxOut) == 0 {
		t.Fatal("expected at least one tx output")
	}
	if !bytes.Equal(unsigned.Tx.TxOut[0].PkScript, unsigned.CommitOutputScript) {
		t.Fatal("first tx output does not use commit output script")
	}
	if unsigned.Reveal.Tx == nil {
		t.Fatal("Reveal.Tx is nil")
	}
	if len(unsigned.Reveal.Tx.TxIn) != 1 || len(unsigned.Reveal.Tx.TxOut) != 1 {
		t.Fatalf("Reveal.Tx shape = %d inputs/%d outputs, want 1/1", len(unsigned.Reveal.Tx.TxIn), len(unsigned.Reveal.Tx.TxOut))
	}
	if unsigned.Reveal.CommitPrevOutput == nil {
		t.Fatal("Reveal.CommitPrevOutput is nil")
	}
	if unsigned.Reveal.CommitPrevOutput.Value != DefaultRevealPostage {
		t.Fatalf("Reveal.CommitPrevOutput.Value = %d, want %d", unsigned.Reveal.CommitPrevOutput.Value, DefaultRevealPostage)
	}
	if !bytes.Equal(unsigned.Reveal.CommitPrevOutput.PkScript, unsigned.CommitOutputScript) {
		t.Fatal("Reveal.CommitPrevOutput.PkScript does not match commit output script")
	}
	if !bytes.Equal(unsigned.Reveal.Tx.TxIn[0].Witness[0], expectedSpendPlan.RevealScript) {
		t.Fatal("Reveal tx witness reveal script does not match commit plan")
	}
	if !bytes.Equal(unsigned.Reveal.Tx.TxIn[0].Witness[1], expectedControlBlock) {
		t.Fatal("Reveal tx witness control block does not match commit plan")
	}
}

func TestBuildRevealTxUsesProvidedRecipientAndValue(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	changeAddr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address(change) error = %v", err)
	}
	revealRecipient, err := keyMaterial.Address(mainnetParams(), "p2tr")
	if err != nil {
		t.Fatalf("Address(reveal) error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}

	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "20112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         2,
			AmountSat:    8000,
			Address:      changeAddr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     changeAddr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: 1000,
		ChangeValue:       5546,
		RevealOutputValue: 700,
		RevealRecipient:   revealRecipient,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if unsigned.Reveal.RecipientAddress != revealRecipient {
		t.Fatalf("Reveal.RecipientAddress = %q, want %q", unsigned.Reveal.RecipientAddress, revealRecipient)
	}
	if unsigned.Reveal.TxOut == nil {
		t.Fatal("Reveal.TxOut is nil")
	}
	if unsigned.Reveal.TxOut.Value != 700 {
		t.Fatalf("Reveal.TxOut.Value = %d, want 700", unsigned.Reveal.TxOut.Value)
	}
	if unsigned.Reveal.TxOut.Value != unsigned.Reveal.RecipientValue {
		t.Fatalf("Reveal.TxOut.Value = %d, want RecipientValue %d", unsigned.Reveal.TxOut.Value, unsigned.Reveal.RecipientValue)
	}
	if unsigned.Reveal.FeeValue != 300 {
		t.Fatalf("Reveal.FeeValue = %d, want 300", unsigned.Reveal.FeeValue)
	}
	if unsigned.Reveal.TxIn == nil {
		t.Fatal("Reveal.TxIn is nil")
	}
	if unsigned.Reveal.TxIn.PreviousOutPoint.Index != 0 {
		t.Fatalf("Reveal.TxIn.PreviousOutPoint.Index = %d, want 0", unsigned.Reveal.TxIn.PreviousOutPoint.Index)
	}
	if unsigned.Reveal.EstimatedVBytes <= 0 {
		t.Fatalf("Reveal.EstimatedVBytes = %d, want > 0", unsigned.Reveal.EstimatedVBytes)
	}
	if unsigned.Reveal.RawTxHex == "" {
		t.Fatal("Reveal.RawTxHex is empty")
	}
}

func TestBuildPlansRevealRecipientDefaultsToChangeAddress(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}

	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "10112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if unsigned.Reveal.RecipientAddress != addr {
		t.Fatalf("Reveal.RecipientAddress = %q, want %q", unsigned.Reveal.RecipientAddress, addr)
	}
}

func TestBuildRejectsRevealValueExceedingCommitValue(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}

	_, err = Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "30112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    9000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       7000,
		RevealOutputValue: DefaultRevealPostage + 1,
	})
	if err == nil {
		t.Fatal("expected Build() to fail when reveal output exceeds commit output")
	}
}

func TestRevealWitnessCommitsToTaprootOutput(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,reveal,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}

	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "40112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if err := unsigned.Reveal.ScriptSpend.ValidateControlBlock(); err != nil {
		t.Fatalf("ValidateControlBlock() error = %v", err)
	}
	controlBlock, err := txscript.ParseControlBlock(unsigned.Reveal.ControlBlock)
	if err != nil {
		t.Fatalf("ParseControlBlock() error = %v", err)
	}
	if err := txscript.VerifyTaprootLeafCommitment(controlBlock, unsigned.Reveal.WitnessProgram, unsigned.Reveal.ScriptSpend.TapLeaf.Script); err != nil {
		t.Fatalf("VerifyTaprootLeafCommitment() error = %v", err)
	}
	if !bytes.Equal(unsigned.Reveal.WitnessProgram, schnorr.SerializePubKey(unsigned.Reveal.ScriptSpend.OutputKey)) {
		t.Fatal("Reveal.WitnessProgram does not match script spend output key")
	}
}

func TestRevealWitnessControlBlockMismatchFailsVerification(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,reveal,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}

	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "50112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	mutated := append([]byte(nil), unsigned.Reveal.ControlBlock...)
	mutated[len(mutated)-1] ^= 0x01
	controlBlock, err := txscript.ParseControlBlock(mutated)
	if err != nil {
		t.Fatalf("ParseControlBlock(mutated) error = %v", err)
	}
	if err := txscript.VerifyTaprootLeafCommitment(controlBlock, unsigned.Reveal.WitnessProgram, unsigned.Reveal.ScriptSpend.TapLeaf.Script); err == nil {
		t.Fatal("expected VerifyTaprootLeafCommitment() to fail for mismatched control block")
	}
}

func TestFinalizeRevealFromCommitHexUpdatesOutpoint(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,prove,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: 1000,
		ChangeValue:       3000,
		RevealOutputValue: 700,
		RevealRecipient:   addr,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	signedCommit, err := Sign(unsigned, keyMaterial)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	finalized, err := FinalizeRevealFromCommitHex(unsigned.Reveal, signedCommit)
	if err != nil {
		t.Fatalf("FinalizeRevealFromCommitHex() error = %v", err)
	}
	_, commitTxID, err := decodeSignedTxHex(signedCommit)
	if err != nil {
		t.Fatalf("decodeSignedTxHex(commit) error = %v", err)
	}
	if finalized.CommitTxID != commitTxID {
		t.Fatalf("Reveal.CommitTxID = %q, want %q", finalized.CommitTxID, commitTxID)
	}
	if finalized.TxIn == nil {
		t.Fatal("Reveal.TxIn is nil")
	}
	if finalized.TxIn.PreviousOutPoint.Hash.String() != commitTxID {
		t.Fatalf("Reveal previous outpoint txid = %q, want %q", finalized.TxIn.PreviousOutPoint.Hash.String(), commitTxID)
	}
	if finalized.RawTxHex == unsigned.Reveal.RawTxHex {
		t.Fatal("expected finalized reveal hex to differ from placeholder reveal hex")
	}
	decodedReveal, revealTxID, err := decodeSignedTxHex(finalized.RawTxHex)
	if err != nil {
		t.Fatalf("decodeSignedTxHex(reveal) error = %v", err)
	}
	if revealTxID == "" {
		t.Fatal("reveal txid is empty")
	}
	if len(decodedReveal.TxIn) != 1 {
		t.Fatalf("reveal input count = %d, want 1", len(decodedReveal.TxIn))
	}
	if decodedReveal.TxIn[0].PreviousOutPoint.Hash.String() != commitTxID {
		t.Fatalf("serialized reveal previous outpoint txid = %q, want %q", decodedReveal.TxIn[0].PreviousOutPoint.Hash.String(), commitTxID)
	}
}

func TestSignP2WPKHProducesVerifiableWitness(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkHashScript, err := p2wpkhScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2wpkhScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,prove,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkHashScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	_, err = Sign(unsigned, keyMaterial)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	prevFetcher, err := PreparePrevOutputFetcher(unsigned.SelectedInputs)
	if err != nil {
		t.Fatalf("PreparePrevOutputFetcher() error = %v", err)
	}
	vm, err := txscript.NewEngine(pkHashScript, unsigned.Tx, 0, txscript.StandardVerifyFlags, nil, txscript.NewTxSigHashes(unsigned.Tx, prevFetcher), 5000, prevFetcher)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestSignP2TRProducesWitness(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	addr, err := keyMaterial.Address(mainnetParams(), "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	pkScript, err := p2trScript(keyMaterial)
	if err != nil {
		t.Fatalf("p2trScript() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000",
			Vout:         1,
			AmountSat:    7000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(pkScript),
			AddressType:  "p2tr",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyMaterial.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       5000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	_, err = Sign(unsigned, keyMaterial)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if len(unsigned.Tx.TxIn[0].Witness) != 1 {
		t.Fatalf("taproot witness items = %d, want 1", len(unsigned.Tx.TxIn[0].Witness))
	}
	if len(unsigned.Tx.TxIn[0].Witness[0]) != 64 {
		t.Fatalf("taproot signature len = %d, want 64", len(unsigned.Tx.TxIn[0].Witness[0]))
	}
}

func TestSignRejectsMismatchedP2WPKHScript(t *testing.T) {
	keyA, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load(keyA) error = %v", err)
	}
	keyB, err := keys.Load("", "111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000")
	if err != nil {
		t.Fatalf("Load(keyB) error = %v", err)
	}
	addr, err := keyA.Address(mainnetParams(), "p2wpkh")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	wrongScript, err := p2wpkhScript(keyB)
	if err != nil {
		t.Fatalf("p2wpkhScript(keyB) error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,prove,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Vout:         0,
			AmountSat:    5000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(wrongScript),
			AddressType:  "p2wpkh",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyA.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       3000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	_, err = Sign(unsigned, keyA)
	if err == nil {
		t.Fatal("expected Sign() to fail for mismatched p2wpkh script")
	}
}

func decodeSignedTxHex(signedHex string) (*wire.MsgTx, string, error) {
	rawTx, err := hex.DecodeString(signedHex)
	if err != nil {
		return nil, "", err
	}
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(rawTx)); err != nil {
		return nil, "", err
	}
	return &tx, tx.TxHash().String(), nil
}

func TestSignRejectsMismatchedP2TRScript(t *testing.T) {
	keyA, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load(keyA) error = %v", err)
	}
	keyB, err := keys.Load("", "111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000")
	if err != nil {
		t.Fatalf("Load(keyB) error = %v", err)
	}
	addr, err := keyA.Address(mainnetParams(), "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	wrongScript, err := p2trScript(keyB)
	if err != nil {
		t.Fatalf("p2trScript(keyB) error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	unsigned, err := Build(BuildInput{
		Inputs: []model.UTXO{{
			TxID:         "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Vout:         1,
			AmountSat:    7000,
			Address:      addr,
			ScriptPubKey: hex.EncodeToString(wrongScript),
			AddressType:  "p2tr",
		}},
		ChangeAddress:     addr,
		CommitPlan:        env.CommitPlan(keyA.PublicKey),
		FeeRateSatVB:      2,
		CommitOutputValue: DefaultRevealPostage,
		ChangeValue:       5000,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	_, err = Sign(unsigned, keyA)
	if err == nil {
		t.Fatal("expected Sign() to fail for mismatched p2tr script")
	}
}
