package bitcoinrpc

import "context"

type BlockHeader struct {
	Hash          string `json:"hash"`
	Height        uint64 `json:"height"`
	Confirmations int64  `json:"confirmations"`
	Version       uint32 `json:"version"`
	PreviousHash  string `json:"previousblockhash"`
}

type VerboseBlock struct {
	Hash          string   `json:"hash"`
	Height        uint64   `json:"height"`
	Confirmations int64    `json:"confirmations"`
	Version       uint32   `json:"version"`
	Tx            []string `json:"tx"`
}

func (c *Client) GetBlockHeader(ctx context.Context, blockHash string) (BlockHeader, error) {
	var result BlockHeader
	if err := c.call(ctx, "getblockheader", []any{blockHash, true}, &result); err != nil {
		return BlockHeader{}, err
	}
	return result, nil
}

func (c *Client) GetBlock(ctx context.Context, blockHash string) (VerboseBlock, error) {
	var result VerboseBlock
	if err := c.call(ctx, "getblock", []any{blockHash, 1}, &result); err != nil {
		return VerboseBlock{}, err
	}
	return result, nil
}
