package protocol

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func ComputeProveHash(indexerID, blockHashHex, stateHashHex string) (string, error) {
	payload := strings.ToLower(strings.TrimSpace(indexerID)) + ":" +
		strings.ToLower(strings.TrimSpace(blockHashHex)) + ":" +
		strings.ToLower(strings.TrimSpace(stateHashHex))
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:]), nil
}
