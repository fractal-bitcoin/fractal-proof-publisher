package inscription

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

func testInternalKey() *btcec.PublicKey {
	_, pub := btcec.PrivKeyFromBytes([]byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	})
	return pub
}

func TestNewTextEnvelopeScriptParts(t *testing.T) {
	env, err := NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	script, err := env.ScriptParts()
	if err != nil {
		t.Fatalf("ScriptParts() error = %v", err)
	}
	if len(script) == 0 {
		t.Fatal("script is empty")
	}
	reveal, err := env.RevealScript().Script()
	if err != nil {
		t.Fatalf("RevealScript().Script() error = %v", err)
	}
	if !bytes.Equal(script, reveal) {
		t.Fatal("envelope script and reveal script differ")
	}
}

func TestRevealScriptChunksLongBody(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 1200)
	env, err := NewTextEnvelope(body)
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	chunks := chunkBytes(env.Body, maxScriptDataChunk)
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3", len(chunks))
	}
	if len(chunks[0]) != 520 || len(chunks[1]) != 520 || len(chunks[2]) != 160 {
		t.Fatalf("unexpected chunk sizes: %d %d %d", len(chunks[0]), len(chunks[1]), len(chunks[2]))
	}
	if _, err := env.RevealScript().Script(); err != nil {
		t.Fatalf("RevealScript().Script() error = %v", err)
	}
}

func TestCommitPlanDerivesTaprootAddress(t *testing.T) {
	body := []byte("fip101,v1,register,100,p2tr,bc1...,name")
	env, err := NewTextEnvelope(body)
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	plan := env.CommitPlan(testInternalKey())
	addr, err := plan.Address(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("CommitPlan.Address() error = %v", err)
	}
	if addr == "" {
		t.Fatal("commit plan address is empty")
	}
}

func TestCommitPlanOutputScriptMatchesAddress(t *testing.T) {
	body := []byte("fip101,v1,register,100,p2tr,bc1...,name")
	env, err := NewTextEnvelope(body)
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	plan := env.CommitPlan(testInternalKey())

	address, err := plan.Address(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("CommitPlan.Address() error = %v", err)
	}
	script, err := plan.OutputScript(&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("CommitPlan.OutputScript() error = %v", err)
	}
	if len(script) == 0 {
		t.Fatal("commit plan output script is empty")
	}

	decoded, err := btcutil.DecodeAddress(address, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("DecodeAddress() error = %v", err)
	}
	expectedScript, err := txscript.PayToAddrScript(decoded)
	if err != nil {
		t.Fatalf("PayToAddrScript() error = %v", err)
	}
	if !bytes.Equal(script, expectedScript) {
		t.Fatal("commit plan output script does not match derived address")
	}
}

func TestCommitPlanScriptSpendPlanIncludesControlBlock(t *testing.T) {
	body := []byte("fip101,v1,register,100,p2tr,bc1...,name")
	env, err := NewTextEnvelope(body)
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	plan := env.CommitPlan(testInternalKey())
	spendPlan, err := plan.ScriptSpendPlan()
	if err != nil {
		t.Fatalf("ScriptSpendPlan() error = %v", err)
	}
	if len(spendPlan.RevealScript) == 0 {
		t.Fatal("reveal script is empty")
	}
	if len(spendPlan.LeafHash) != 32 {
		t.Fatalf("leaf hash len = %d, want 32", len(spendPlan.LeafHash))
	}
	if len(spendPlan.MerkleRoot) != 32 {
		t.Fatalf("merkle root len = %d, want 32", len(spendPlan.MerkleRoot))
	}
	controlBlock, err := spendPlan.ControlBlockBytes()
	if err != nil {
		t.Fatalf("ControlBlockBytes() error = %v", err)
	}
	if len(controlBlock) != 33 {
		t.Fatalf("control block len = %d, want 33", len(controlBlock))
	}
	if controlBlock[0] != byte(spendPlan.TapLeaf.LeafVersion) && controlBlock[0] != byte(spendPlan.TapLeaf.LeafVersion)|0x01 {
		t.Fatalf("unexpected control block first byte: %x", controlBlock[0])
	}
	if len(controlBlock[1:]) != 32 {
		t.Fatalf("control block x-only internal key len = %d, want 32", len(controlBlock[1:]))
	}
}

func TestScriptSpendPlanWitnessStackIncludesScriptAndControlBlock(t *testing.T) {
	env, err := NewTextEnvelope([]byte("fip101,v1,register,100,p2tr,bc1...,name"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	spendPlan, err := env.CommitPlan(testInternalKey()).ScriptSpendPlan()
	if err != nil {
		t.Fatalf("ScriptSpendPlan() error = %v", err)
	}
	annex := []byte{0x50, 0xaa}
	dummySig := bytes.Repeat([]byte{0x31}, 64)
	witness, err := spendPlan.WitnessStack(dummySig, annex)
	if err != nil {
		t.Fatalf("WitnessStack() error = %v", err)
	}
	if len(witness) != 4 {
		t.Fatalf("witness item count = %d, want 4", len(witness))
	}
	if !bytes.Equal(witness[0], dummySig) {
		t.Fatal("first witness item does not preserve script witness data")
	}
	if !bytes.Equal(witness[1], annex) {
		t.Fatal("second witness item does not preserve script witness data")
	}
	if !bytes.Equal(witness[2], spendPlan.RevealScript) {
		t.Fatal("reveal script missing from witness stack")
	}
	controlBlock, err := spendPlan.ControlBlockBytes()
	if err != nil {
		t.Fatalf("ControlBlockBytes() error = %v", err)
	}
	if !bytes.Equal(witness[3], controlBlock) {
		t.Fatal("control block missing from witness stack")
	}
}

func TestScriptSpendPlanEstimatedWitnessSizeCountsVarInts(t *testing.T) {
	env, err := NewTextEnvelope(bytes.Repeat([]byte("z"), 600))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	spendPlan, err := env.CommitPlan(testInternalKey()).ScriptSpendPlan()
	if err != nil {
		t.Fatalf("ScriptSpendPlan() error = %v", err)
	}
	witness, err := spendPlan.WitnessStack([]byte{0x20, 0x21, 0x22})
	if err != nil {
		t.Fatalf("WitnessStack() error = %v", err)
	}
	got, err := spendPlan.EstimatedWitnessSize([]byte{0x20, 0x21, 0x22})
	if err != nil {
		t.Fatalf("EstimatedWitnessSize() error = %v", err)
	}
	var want int64 = int64(1)
	for _, item := range witness {
		want += int64(1)
		if len(item) > 252 {
			want += 2
		}
		want += int64(len(item))
	}
	if got != want {
		t.Fatalf("EstimatedWitnessSize() = %d, want %d", got, want)
	}
}

func TestControlBlockBytesRejectsInvalidInclusionProofLength(t *testing.T) {
	plan := ControlBlockPlan{InternalKey: testInternalKey(), InclusionProof: []byte{0x01}}
	if _, err := plan.Bytes(); err == nil {
		t.Fatal("expected Bytes() to fail for invalid inclusion proof length")
	}
}

func TestCommitPlanOutputKeyRequiresInternalKey(t *testing.T) {
	plan := CommitPlan{}
	if _, err := plan.OutputKey(); err == nil {
		t.Fatal("expected OutputKey() to fail without internal key")
	}
}

func TestControlBlockBytesRequiresInternalKey(t *testing.T) {
	plan := ControlBlockPlan{}
	if _, err := plan.Bytes(); err == nil {
		t.Fatal("expected Bytes() to fail without internal key")
	}
}
