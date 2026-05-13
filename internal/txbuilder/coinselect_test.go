package txbuilder

import (
	"testing"

	"fractal-proof-publisher/internal/inscription"
	"fractal-proof-publisher/internal/keys"
	"fractal-proof-publisher/internal/model"
)

func TestEstimateRevealFeeAndCommitFundingCloseValueLoop(t *testing.T) {
	keyMaterial, err := keys.Load("", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	recipient, err := keyMaterial.Address(mainnetParams(), "p2tr")
	if err != nil {
		t.Fatalf("Address() error = %v", err)
	}
	env, err := inscription.NewTextEnvelope([]byte("fip101,v1,prove,100:1,100,00112233445566778899aabbccddeeff00112233"))
	if err != nil {
		t.Fatalf("NewTextEnvelope() error = %v", err)
	}
	revealVBytes, revealFee, err := EstimateRevealFee(env.CommitPlan(keyMaterial.PublicKey), mainnetParams().Name, recipient, 8)
	if err != nil {
		t.Fatalf("EstimateRevealFee() error = %v", err)
	}
	if revealVBytes <= 0 {
		t.Fatalf("reveal vbytes = %d, want > 0", revealVBytes)
	}
	if revealFee <= 0 {
		t.Fatalf("reveal fee = %d, want > 0", revealFee)
	}

	funding, err := PlanFunding([]model.UTXO{{
		TxID:        "a",
		AmountSat:   5000,
		AddressType: "p2wpkh",
	}}, 8, DefaultRevealPostage+revealFee, DefaultSendChangeMin)
	if err != nil {
		t.Fatalf("PlanFunding() error = %v", err)
	}
	if funding.CommitOutputValue != DefaultRevealPostage+revealFee {
		t.Fatalf("commit output value = %d, want %d", funding.CommitOutputValue, DefaultRevealPostage+revealFee)
	}
	if funding.FeeValue+funding.CommitOutputValue+funding.ChangeValue != 5000 {
		t.Fatalf("funding conservation mismatch: fee=%d commit=%d change=%d total=%d", funding.FeeValue, funding.CommitOutputValue, funding.ChangeValue, 5000)
	}
}

func TestSelectInputs(t *testing.T) {
	selected, total, err := SelectInputs([]model.UTXO{
		{TxID: "a", AmountSat: 400},
		{TxID: "b", AmountSat: 700},
	}, 1000)
	if err != nil {
		t.Fatalf("SelectInputs() error = %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected len = %d, want 2", len(selected))
	}
	if total != 1100 {
		t.Fatalf("total = %d, want 1100", total)
	}
}

func TestPlanFundingIncludesEstimatedFeeAndChange(t *testing.T) {
	funding, err := PlanFunding([]model.UTXO{{
		TxID:        "a",
		AmountSat:   5000,
		AddressType: "p2wpkh",
	}}, 8, DefaultRevealPostage, DefaultSendChangeMin)
	if err != nil {
		t.Fatalf("PlanFunding() error = %v", err)
	}
	if len(funding.SelectedInputs) != 1 {
		t.Fatalf("selected len = %d, want 1", len(funding.SelectedInputs))
	}
	if funding.TotalInputValue != 5000 {
		t.Fatalf("total input value = %d, want 5000", funding.TotalInputValue)
	}
	if funding.EstimatedVBytes != 165 {
		t.Fatalf("estimated vbytes = %d, want 165", funding.EstimatedVBytes)
	}
	if funding.FeeValue != 1320 {
		t.Fatalf("fee value = %d, want 1320", funding.FeeValue)
	}
	if funding.ChangeValue != 3134 {
		t.Fatalf("change value = %d, want 3134", funding.ChangeValue)
	}
}

func TestPlanFundingDropsDustChangeIntoFee(t *testing.T) {
	funding, err := PlanFunding([]model.UTXO{{
		TxID:        "a",
		AmountSat:   1000,
		AddressType: "p2wpkh",
	}}, 1, DefaultRevealPostage, DefaultSendChangeMin)
	if err != nil {
		t.Fatalf("PlanFunding() error = %v", err)
	}
	if funding.ChangeValue != 0 {
		t.Fatalf("change value = %d, want 0", funding.ChangeValue)
	}
	if funding.FeeValue != 454 {
		t.Fatalf("fee value = %d, want 454", funding.FeeValue)
	}
	if funding.EstimatedVBytes != 122 {
		t.Fatalf("estimated vbytes = %d, want 122", funding.EstimatedVBytes)
	}
}
