package service

import (
	"fmt"
	"log"
	"strings"

	"fractal-proof-publisher/internal/store"
)

func (e *Engine) Logf(format string, args ...any) {
	log.Printf("publisher %s", fmt.Sprintf(format, args...))
}

func (e *Engine) LogMessagef(message store.MessageRecord, format string, args ...any) {
	fields := []string{
		fmt.Sprintf("message_id=%d", message.ID),
		fmt.Sprintf("type=%s", message.Type),
		fmt.Sprintf("status=%s", message.Status),
	}
	if message.RelatedHeight != 0 {
		fields = append(fields, fmt.Sprintf("height=%d", message.RelatedHeight))
	}
	if message.IndexerID != "" {
		fields = append(fields, fmt.Sprintf("indexer_id=%s", message.IndexerID))
	}
	if message.TxID != "" {
		fields = append(fields, fmt.Sprintf("commit_txid=%s", message.TxID))
	}
	if message.ConfirmHeight != 0 {
		fields = append(fields, fmt.Sprintf("commit_height=%d", message.ConfirmHeight))
	}
	if message.RevealTxID != "" {
		fields = append(fields, fmt.Sprintf("reveal_txid=%s", message.RevealTxID))
	}
	if message.RevealConfirmHeight != 0 {
		fields = append(fields, fmt.Sprintf("reveal_height=%d", message.RevealConfirmHeight))
	}

	msg := strings.Join(fields, " ")
	if format != "" {
		msg += " " + fmt.Sprintf(format, args...)
	}
	e.Logf("%s", msg)
}
