package protocol

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"fractal-proof-publisher/internal/model"
)

const (
	ProtocolName    = "fip101"
	ProtocolVersion = "1"
	OpRegister      = "register_indexer"
	OpProve         = "submit_proof"
)

func EncodeRegisterText(data model.RegisterData) ([]byte, error) {
	name := truncateUTF8ByBytes(data.Name, 64)
	line := strings.Join([]string{
		ProtocolName,
		ProtocolVersion,
		OpRegister,
		strconv.FormatUint(uint64(data.IndexRatioBP), 10),
		data.RewardAddr,
		name,
	}, ",")
	return []byte(line), nil
}

func EncodeProveText(data model.ProveData) ([]byte, error) {
	if data.IndexerID == "" {
		return nil, fmt.Errorf("indexer id is required")
	}
	if len(data.ProveHash) != 64 {
		return nil, fmt.Errorf("prove hash must be 64 hex chars")
	}
	if _, err := hex.DecodeString(data.ProveHash); err != nil {
		return nil, fmt.Errorf("decode prove hash: %w", err)
	}

	line := strings.Join([]string{
		ProtocolName,
		ProtocolVersion,
		OpProve,
		data.IndexerID,
		strconv.FormatUint(data.ProveHeight, 10),
		strings.ToLower(data.ProveHash),
	}, ",")
	return []byte(line), nil
}

func truncateUTF8ByBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	b := []byte(s)
	for i := max; i >= 0; i-- {
		if utf8.Valid(b[:i]) {
			return string(b[:i])
		}
	}
	return ""
}
