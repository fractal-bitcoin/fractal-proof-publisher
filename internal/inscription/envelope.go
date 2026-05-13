package inscription

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

const maxScriptDataChunk = 520

type Envelope struct {
	ContentType string
	Body        []byte
	Protocol    string
}

type Payload struct {
	ContentType string
	Protocol    string
	Body        []byte
}

type RevealScript struct {
	Payload           Payload
	SignerPubKey      *btcec.PublicKey
	SignerAddressType string
}

type ControlBlockPlan struct {
	InternalKey     *btcec.PublicKey
	LeafVersion     txscript.TapscriptLeafVersion
	OutputKeyYIsOdd bool
	InclusionProof  []byte
}

type ScriptSpendPlan struct {
	RevealScript []byte
	TapLeaf      txscript.TapLeaf
	LeafHash     []byte
	MerkleRoot   []byte
	OutputKey    *btcec.PublicKey
	ControlBlock ControlBlockPlan
}

type CommitPlan struct {
	InternalKey *btcec.PublicKey
	Reveal      RevealScript
}

func NewTextEnvelope(body []byte) (Envelope, error) {
	if len(body) == 0 {
		return Envelope{}, fmt.Errorf("inscription body is required")
	}
	return Envelope{
		ContentType: "text/plain;charset=utf-8",
		Body:        body,
		Protocol:    "fip101",
	}, nil
}

func (e Envelope) Payload() Payload {
	return Payload{ContentType: e.ContentType, Protocol: e.Protocol, Body: e.Body}
}

func (e Envelope) RevealScript() RevealScript {
	return RevealScript{Payload: e.Payload()}
}

func (e Envelope) ScriptParts() ([]byte, error) {
	return e.RevealScript().Script()
}

func (e Envelope) CommitPlan(internalKey *btcec.PublicKey, signerAddressType ...string) CommitPlan {
	reveal := RevealScript{Payload: e.Payload(), SignerPubKey: internalKey}
	if len(signerAddressType) > 0 {
		reveal.SignerAddressType = signerAddressType[0]
	}
	return CommitPlan{InternalKey: internalKey, Reveal: reveal}
}

func (r RevealScript) Script() ([]byte, error) {
	builder := txscript.NewScriptBuilder()
	if r.SignerPubKey != nil {
		typeOpcode, err := signerAddressTypeOpcode(r.SignerPubKey, r.SignerAddressType)
		if err != nil {
			return nil, err
		}
		builder.AddData(schnorr.SerializePubKey(r.SignerPubKey))
		builder.AddOp(txscript.OP_CHECKSIGVERIFY)
		builder.AddOp(typeOpcode)
	}
	builder.AddOp(txscript.OP_FALSE)
	builder.AddOp(txscript.OP_IF)
	builder.AddData([]byte("ord"))
	builder.AddData([]byte{1})
	builder.AddData([]byte(r.Payload.ContentType))
	builder.AddData([]byte{7})
	builder.AddData([]byte(r.Payload.Protocol))
	builder.AddOp(txscript.OP_0)
	for _, chunk := range chunkBytes(r.Payload.Body, maxScriptDataChunk) {
		builder.AddData(chunk)
	}
	builder.AddOp(txscript.OP_ENDIF)
	script, err := builder.Script()
	if err != nil {
		return nil, fmt.Errorf("build inscription reveal script: %w", err)
	}
	return script, nil
}

func signerAddressTypeOpcode(pubKey *btcec.PublicKey, addressType string) (byte, error) {
	if pubKey == nil {
		return 0, fmt.Errorf("signer pubkey is required")
	}

	compressed := pubKey.SerializeCompressed()
	isOdd := len(compressed) > 0 && compressed[0] == 0x03

	var code byte
	switch addressType {
	case "", "p2tr":
		code = 8
	case "p2wpkh":
		if isOdd {
			code = 3
		} else {
			code = 2
		}
	case "p2pkh":
		if isOdd {
			code = 5
		} else {
			code = 4
		}
	case "p2sh-p2wpkh":
		if isOdd {
			code = 7
		} else {
			code = 6
		}
	default:
		return 0, fmt.Errorf("unsupported signer address type: %s", addressType)
	}

	return txscript.OP_1 - 1 + code, nil
}

func (c ControlBlockPlan) Validate() error {
	if c.InternalKey == nil {
		return fmt.Errorf("control block internal key is required")
	}
	if len(c.InclusionProof)%32 != 0 {
		return fmt.Errorf("control block inclusion proof must be a multiple of 32 bytes")
	}
	return nil
}

func (c ControlBlockPlan) Bytes() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	controlByte := byte(c.LeafVersion)
	if c.OutputKeyYIsOdd {
		controlByte |= 0x01
	}
	controlBlock := make([]byte, 1, 33+len(c.InclusionProof))
	controlBlock[0] = controlByte
	controlBlock = append(controlBlock, schnorr.SerializePubKey(c.InternalKey)...)
	controlBlock = append(controlBlock, c.InclusionProof...)
	return controlBlock, nil
}

func (s ScriptSpendPlan) ControlBlockBytes() ([]byte, error) {
	return s.ControlBlock.Bytes()
}

func (s ScriptSpendPlan) WitnessStack(scriptWitness ...[]byte) (wire.TxWitness, error) {
	controlBlock, err := s.ControlBlockBytes()
	if err != nil {
		return nil, err
	}
	witness := make(wire.TxWitness, 0, len(scriptWitness)+2)
	for _, item := range scriptWitness {
		witness = append(witness, append([]byte(nil), item...))
	}
	witness = append(witness,
		append([]byte(nil), s.RevealScript...),
		append([]byte(nil), controlBlock...),
	)
	return witness, nil
}

func (s ScriptSpendPlan) ValidateControlBlock() error {
	controlBlockBytes, err := s.ControlBlockBytes()
	if err != nil {
		return err
	}
	controlBlock, err := txscript.ParseControlBlock(controlBlockBytes)
	if err != nil {
		return fmt.Errorf("parse control block: %w", err)
	}
	if s.OutputKey == nil {
		return fmt.Errorf("script spend output key is required")
	}
	if err := txscript.VerifyTaprootLeafCommitment(controlBlock, schnorr.SerializePubKey(s.OutputKey), s.TapLeaf.Script); err != nil {
		return fmt.Errorf("verify taproot leaf commitment: %w", err)
	}
	return nil
}

func (s ScriptSpendPlan) WitnessProgram() ([]byte, error) {
	if s.OutputKey == nil {
		return nil, fmt.Errorf("script spend output key is required")
	}
	if err := s.ValidateControlBlock(); err != nil {
		return nil, err
	}
	return schnorr.SerializePubKey(s.OutputKey), nil
}

func (s ScriptSpendPlan) EstimatedWitnessSize(scriptWitness ...[]byte) (int64, error) {
	witness, err := s.WitnessStack(scriptWitness...)
	if err != nil {
		return 0, err
	}
	size := wire.VarIntSerializeSize(uint64(len(witness)))
	for _, item := range witness {
		size += wire.VarIntSerializeSize(uint64(len(item)))
		size += len(item)
	}
	return int64(size), nil
}

func (c CommitPlan) TapLeaf() (txscript.TapLeaf, error) {
	revealScript, err := c.Reveal.Script()
	if err != nil {
		return txscript.TapLeaf{}, err
	}
	return txscript.NewBaseTapLeaf(revealScript), nil
}

func (c CommitPlan) ScriptSpendPlan() (ScriptSpendPlan, error) {
	if c.InternalKey == nil {
		return ScriptSpendPlan{}, fmt.Errorf("commit plan internal key is required")
	}
	leaf, err := c.TapLeaf()
	if err != nil {
		return ScriptSpendPlan{}, err
	}
	tree := txscript.AssembleTaprootScriptTree(leaf)
	rootHash := tree.RootNode.TapHash()
	leafHash := leaf.TapHash()
	outputKey := txscript.ComputeTaprootOutputKey(c.InternalKey, rootHash[:])
	revealScript, err := c.Reveal.Script()
	if err != nil {
		return ScriptSpendPlan{}, err
	}
	return ScriptSpendPlan{
		RevealScript: revealScript,
		TapLeaf:      leaf,
		LeafHash:     append([]byte(nil), leafHash[:]...),
		MerkleRoot:   append([]byte(nil), rootHash[:]...),
		OutputKey:    outputKey,
		ControlBlock: ControlBlockPlan{
			InternalKey:     c.InternalKey,
			LeafVersion:     leaf.LeafVersion,
			OutputKeyYIsOdd: outputKey.SerializeCompressed()[0] == secp256k1CompressedOdd,
		},
	}, nil
}

func (c CommitPlan) OutputKey() (*btcec.PublicKey, error) {
	spendPlan, err := c.ScriptSpendPlan()
	if err != nil {
		return nil, err
	}
	return spendPlan.OutputKey, nil
}

func (c CommitPlan) OutputScript(params *chaincfg.Params) ([]byte, error) {
	outputKey, err := c.OutputKey()
	if err != nil {
		return nil, err
	}
	addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(outputKey), params)
	if err != nil {
		return nil, err
	}
	script, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, fmt.Errorf("build commit plan output script: %w", err)
	}
	return script, nil
}

func (c CommitPlan) Address(params *chaincfg.Params) (string, error) {
	outputKey, err := c.OutputKey()
	if err != nil {
		return "", err
	}
	addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(outputKey), params)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func chunkBytes(data []byte, size int) [][]byte {
	if len(data) == 0 {
		return nil
	}
	var chunks [][]byte
	for start := 0; start < len(data); start += size {
		end := start + size
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[start:end])
	}
	return chunks
}

const secp256k1CompressedOdd = 0x03
