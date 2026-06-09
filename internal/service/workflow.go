package service

import (
	"context"
	"fmt"

	"fractal-proof-publisher/internal/model"
)

func (e *Engine) RunRegister(ctx context.Context, indexRatioBP uint16, rewardAddrType, rewardAddr, name string) (string, error) {
	e.Logf("register_run start name=%s reward_addr_type=%s index_ratio_bp=%d", name, rewardAddrType, indexRatioBP)
	messageID, err := e.CreateRegisterSubmission(ctx, model.RegisterData{
		IndexRatioBP:   indexRatioBP,
		RewardAddrType: rewardAddrType,
		RewardAddr:     rewardAddr,
		Name:           name,
	})
	if err != nil {
		return "", err
	}
	if err := e.ProgressOnce(ctx); err != nil {
		return "", err
	}
	message, err := e.Store.GetMessage(ctx, messageID)
	if err != nil {
		return "", err
	}
	if message.TxID == "" {
		return "", fmt.Errorf("register submission %d was created but commit txid is empty after progress", messageID)
	}
	e.LogMessagef(message, "register_run complete")
	return message.TxID, nil
}

func (e *Engine) RunProve(ctx context.Context, height uint64, indexerID string) (string, error) {
	e.Logf("prove_run start height=%d indexer_id=%s", height, indexerID)
	messageID, err := e.CreateProveSubmission(ctx, height, indexerID)
	if err != nil {
		return "", err
	}
	if err := e.ProgressOnce(ctx); err != nil {
		return "", err
	}
	message, err := e.Store.GetMessage(ctx, messageID)
	if err != nil {
		return "", err
	}
	if message.TxID == "" {
		return "", fmt.Errorf("prove submission %d was created but commit txid is empty after progress", messageID)
	}
	e.LogMessagef(message, "prove_run complete")
	return message.TxID, nil
}

func (e *Engine) CreateRegisterSubmission(ctx context.Context, data model.RegisterData) (int64, error) {
	return e.CreateRegisterSubmissionFromHeight(ctx, data, nil)
}

func (e *Engine) CreateRegisterSubmissionFromHeight(ctx context.Context, data model.RegisterData, relatedHeight *uint64) (int64, error) {
	messageID, _, err := e.BuildRegisterMessageFromHeight(ctx, data, relatedHeight)
	return messageID, err
}

func (e *Engine) CreateProveSubmission(ctx context.Context, height uint64, indexerID string) (int64, error) {
	messageID, _, err := e.BuildProveMessage(ctx, height, indexerID)
	return messageID, err
}
