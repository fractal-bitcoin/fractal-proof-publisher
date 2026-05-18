package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fractal-proof-publisher/internal/model"
)

const progressRetryDelay = 3 * time.Second
const progressRetryThreshold = 10
const txInputsMissingOrSpentError = "bad-txns-inputs-missingorspent"

func (e *Engine) markProgressFailure() {
	e.ProgressErrCount++
	if e.ProgressErrCount >= progressRetryThreshold {
		e.ProgressRetryAt = time.Now().Add(progressRetryDelay)
		e.Logf(
			"progress_retry_scheduled failures=%d threshold=%d delay=%s",
			e.ProgressErrCount,
			progressRetryThreshold,
			progressRetryDelay,
		)
		e.ProgressErrCount = 0
	}
}

func (e *Engine) clearProgressFailures() {
	e.ProgressErrCount = 0
}

func isTxInputsMissingOrSpent(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), txInputsMissingOrSpentError)
}

func (e *Engine) ConfirmOnce(ctx context.Context) error {
	return e.ProgressOnce(ctx)
}

func (e *Engine) ProgressOnce(ctx context.Context) error {
	if !e.ProgressRetryAt.IsZero() {
		now := time.Now()
		if now.Before(e.ProgressRetryAt) {
			e.Logf("progress_retry_wait remaining=%s threshold=%d", e.ProgressRetryAt.Sub(now).Round(time.Millisecond), progressRetryThreshold)
			return nil
		}
		e.ProgressRetryAt = time.Time{}
	}

	pending, err := e.Store.ListMessagesByStatus(ctx,
		model.MessageStatusBuilding,
		model.MessageStatusCommitSigned,
		model.MessageStatusCommitSent,
		model.MessageStatusCommitConfirmed,
		model.MessageStatusRevealSent,
	)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		e.Logf("progress_pending count=%d", len(pending))
	}
	for _, message := range pending {
		for {
			advanced := false

			switch message.Status {
			case model.MessageStatusBuilding:
				e.LogMessagef(message, "build_sign_transition start")
				signed, err := e.BuildAndSign(ctx, message.ID, message.PayloadText)
				if err != nil {
					e.LogMessagef(message, "build_sign_failed err=%v", err)
					e.markProgressFailure()
					advanced = false
					break
				}
				e.clearProgressFailures()
				message.RawTxHex = signed
				message.Status = model.MessageStatusCommitSigned
				e.LogMessagef(message, "build_sign_succeeded next_status=%s", message.Status)
				advanced = true

			case model.MessageStatusCommitSigned:
				if message.RawTxHex == "" && !e.Config.Runtime.DisableBroadcast {
					advanced = false
					break
				}
				if !e.Config.Runtime.DisableBroadcast && !e.isUnisatOpenAPIMode() && e.RPC == nil {
					advanced = false
					break
				}
				if !e.Config.Runtime.DisableBroadcast && e.isUnisatOpenAPIMode() && e.UnisatOpenAPI == nil {
					advanced = false
					break
				}
				e.LogMessagef(message, "commit_broadcast_transition start")
				txid, err := e.BroadcastSigned(ctx, message.ID, message.RawTxHex)
				if err != nil {
					e.LogMessagef(message, "commit_broadcast_failed err=%v", err)
					if isTxInputsMissingOrSpent(err) {
						rebuilt, rebuildErr := e.Store.ResetMessageToBuilding(ctx, message.ID)
						if rebuildErr != nil {
							return rebuildErr
						}
						if rebuilt {
							message.Status = model.MessageStatusBuilding
							message.TxID = ""
							message.RawTxHex = ""
							message.ConfirmHeight = 0
							message.RevealTxID = ""
							message.RevealRawTxHex = ""
							message.RevealBroadcastAt = ""
							message.RevealConfirmHeight = 0
							e.LogMessagef(message, "commit_broadcast_rebuild_to_building reason=%s", txInputsMissingOrSpentError)
						}
					}
					e.markProgressFailure()
					advanced = false
					break
				}
				e.clearProgressFailures()
				message.TxID = txid
				message.Status = model.MessageStatusCommitSent
				e.LogMessagef(message, "commit_broadcast_succeeded next_status=%s", message.Status)
				advanced = true

			case model.MessageStatusCommitSent:
				if e.isUnisatOpenAPIMode() {
					if e.UnisatOpenAPI == nil || strings.TrimSpace(message.TxID) == "" {
						advanced = false
						break
					}
					found, err := e.UnisatOpenAPI.HasTx(ctx, message.TxID)
					if err != nil {
						e.LogMessagef(message, "commit_confirm_check_failed err=%v", err)
						e.markProgressFailure()
						advanced = false
						break
					}
					e.clearProgressFailures()
					if !found {
						e.LogMessagef(message, "commit_confirm_waiting reason=unisat_tx_not_visible")
						advanced = false
						break
					}
					e.LogMessagef(message, "commit_confirmed_detected source=unisat_open_api")
					if err := e.Store.MarkMessageConfirmed(ctx, message.ID, message.RelatedHeight); err != nil {
						return err
					}
					message.ConfirmHeight = message.RelatedHeight
					message.Status = model.MessageStatusCommitConfirmed
					advanced = true
					break
				}
				if e.RPC == nil || message.TxID == "" {
					advanced = false
					break
				}
				confirmHeight, txIndex, err := e.findConfirmation(ctx, message.RelatedHeight, message.TxID)
				if err != nil {
					return err
				}
				if confirmHeight == 0 && message.Type == model.MessageTypeRegister {
					parts := strings.Split(message.PayloadText, ",")
					if len(parts) >= 3 {
						_ = e.Store.SetChainState(ctx, "register_payload_seen", strconv.FormatInt(message.ID, 10))
					}
				}
				if confirmHeight == 0 {
					advanced = false
					break
				}
				e.LogMessagef(message, "commit_confirmed_detected confirm_height=%d tx_index=%d", confirmHeight, txIndex)
				if err := e.Store.MarkMessageConfirmed(ctx, message.ID, confirmHeight); err != nil {
					return err
				}
				if !e.isUnisatOpenAPIMode() {
					if err := e.Store.MarkUTXOConfirmed(ctx, message.TxID, confirmHeight); err != nil {
						return err
					}
					if err := e.Store.MarkChangeUTXOsConfirmed(ctx, message.ID, confirmHeight); err != nil {
						return err
					}
				}
				if message.Type == model.MessageTypeRegister {
					if err := e.Store.UpdateMessageConfirmationDetails(ctx, message.ID, confirmHeight, ""); err != nil {
						return err
					}
					message.RelatedHeight = confirmHeight
				}
				message.ConfirmHeight = confirmHeight
				message.Status = model.MessageStatusCommitConfirmed
				e.LogMessagef(message, "commit_confirmed_applied next_status=%s", message.Status)
				advanced = true

			case model.MessageStatusCommitConfirmed:
				if message.RevealRawTxHex == "" || message.RevealBroadcastAt != "" {
					advanced = false
					break
				}
				if !e.Config.Runtime.DisableBroadcast && !e.isUnisatOpenAPIMode() && e.RPC == nil {
					advanced = false
					break
				}
				if !e.Config.Runtime.DisableBroadcast && e.isUnisatOpenAPIMode() && e.UnisatOpenAPI == nil {
					advanced = false
					break
				}
				e.LogMessagef(message, "reveal_broadcast_transition start")
				txid, err := e.BroadcastReveal(ctx, message.ID)
				if err != nil {
					e.LogMessagef(message, "reveal_broadcast_failed err=%v", err)
					if e.isUnisatOpenAPIMode() && isTxInputsMissingOrSpent(err) {
						rolledBack, rbErr := e.Store.RollbackMessageToCommitSigned(ctx, message.ID)
						if rbErr != nil {
							return rbErr
						}
						if rolledBack {
							message.Status = model.MessageStatusCommitSigned
							message.TxID = ""
							message.ConfirmHeight = 0
							message.RevealBroadcastAt = ""
							message.RevealConfirmHeight = 0
							e.LogMessagef(message, "reveal_broadcast_rollback_to_commit_signed reason=%s", txInputsMissingOrSpentError)
						}
					}
					e.markProgressFailure()
					advanced = false
					break
				}
				e.clearProgressFailures()
				message.RevealTxID = txid
				message.RevealBroadcastAt = "sent"
				message.Status = model.MessageStatusRevealSent
				e.LogMessagef(message, "reveal_broadcast_succeeded next_status=%s", message.Status)
				advanced = true

			case model.MessageStatusRevealSent:
				if e.isUnisatOpenAPIMode() {
					if e.UnisatOpenAPI == nil || strings.TrimSpace(message.RevealTxID) == "" {
						advanced = false
						break
					}
					found, err := e.UnisatOpenAPI.HasTx(ctx, message.RevealTxID)
					if err != nil {
						e.LogMessagef(message, "reveal_confirm_check_failed err=%v", err)
						e.markProgressFailure()
						advanced = false
						break
					}
					e.clearProgressFailures()
					if !found {
						e.LogMessagef(message, "reveal_confirm_waiting reason=unisat_tx_not_visible retry_push=true")
						txid, err := e.BroadcastReveal(ctx, message.ID)
						if err != nil {
							e.LogMessagef(message, "reveal_rebroadcast_failed err=%v", err)
							e.markProgressFailure()
							advanced = false
							break
						}
						e.clearProgressFailures()
						message.RevealTxID = txid
						message.RevealBroadcastAt = "sent"
						e.LogMessagef(message, "reveal_rebroadcast_succeeded txid=%s", txid)
						advanced = false
						break
					}
					e.LogMessagef(message, "reveal_confirmed_detected source=unisat_open_api")
					if err := e.Store.MarkRevealConfirmed(ctx, message.ID, message.RelatedHeight); err != nil {
						return err
					}
					if message.Type == model.MessageTypeRegister {
						indexerID := strings.TrimSpace(message.IndexerID)
						if indexerID == "" {
							indexerID = strings.TrimSpace(e.Config.Register.IndexerID)
						}
						if indexerID != "" {
							if err := e.Store.SetIndexerID(ctx, indexerID); err != nil {
								return err
							}
							if err := e.Store.UpdateMessageConfirmationDetails(ctx, message.ID, message.RelatedHeight, indexerID); err != nil {
								return err
							}
							message.IndexerID = indexerID
						}
					}
					message.RevealConfirmHeight = message.RelatedHeight
					message.Status = model.MessageStatusDone
					advanced = true
					break
				}
				if e.RPC == nil || message.RevealTxID == "" || message.RevealBroadcastAt == "" || message.RevealConfirmHeight != 0 {
					advanced = false
					break
				}
				confirmHeight, txIndex, err := e.findConfirmation(ctx, message.RelatedHeight, message.RevealTxID)
				if err != nil {
					return err
				}
				if confirmHeight == 0 {
					advanced = false
					break
				}
				e.LogMessagef(message, "reveal_confirmed_detected confirm_height=%d tx_index=%d", confirmHeight, txIndex)
				if err := e.Store.MarkRevealConfirmed(ctx, message.ID, confirmHeight); err != nil {
					return err
				}
				if message.Type == model.MessageTypeRegister {
					indexerID := fmt.Sprintf("%d:%d", confirmHeight, txIndex)
					if err := e.Store.SetIndexerID(ctx, indexerID); err != nil {
						return err
					}
					if err := e.Store.UpdateMessageConfirmationDetails(ctx, message.ID, confirmHeight, indexerID); err != nil {
						return err
					}
					message.IndexerID = indexerID
					message.RelatedHeight = confirmHeight
				}
				message.RevealConfirmHeight = confirmHeight
				message.Status = model.MessageStatusDone
				e.LogMessagef(message, "reveal_confirmed_applied next_status=%s", message.Status)
				advanced = true
			}

			if !advanced {
				break
			}
		}
		if message.Type == model.MessageTypeProve && message.Status != model.MessageStatusDone && !e.ProgressRetryAt.IsZero() {
			e.LogMessagef(message, "progress_blocking_next_prove status=%s", message.Status)
			break
		}
	}
	return nil
}

func (e *Engine) findConfirmation(ctx context.Context, relatedHeight uint64, txid string) (uint64, int, error) {
	startHeight := relatedHeight
	if startHeight == 0 {
		lastScanned, err := e.Store.GetChainState(ctx, "last_scanned_height")
		if err != nil {
			return 0, -1, err
		}
		if lastScanned == "" {
			return 0, -1, nil
		}
		parsed, err := strconv.ParseUint(lastScanned, 10, 64)
		if err != nil {
			return 0, -1, err
		}
		startHeight = parsed
		if e.Config.Scan.MaxReorgDepth > 0 && parsed+1 > e.Config.Scan.MaxReorgDepth {
			startHeight = parsed + 1 - e.Config.Scan.MaxReorgDepth
		}
	}

	endHeight := startHeight
	lastScanned, err := e.Store.GetChainState(ctx, "last_scanned_height")
	if err == nil && lastScanned != "" {
		if parsed, parseErr := strconv.ParseUint(lastScanned, 10, 64); parseErr == nil && parsed > endHeight {
			endHeight = parsed
		}
	}

	for height := startHeight; height <= endHeight; height++ {
		blockHash, err := e.RPC.GetBlockHash(ctx, height)
		if err != nil {
			continue
		}
		block, err := e.RPC.GetBlock(ctx, blockHash)
		if err != nil {
			continue
		}
		for idx, blockTxID := range block.Tx {
			if blockTxID == txid {
				return block.Height, idx, nil
			}
		}
	}
	return 0, -1, nil
}
