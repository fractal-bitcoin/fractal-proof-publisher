package bitcoinrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type Client struct {
	url      string
	user     string
	password string
	http     *http.Client
}

func New(url, user, password string) *Client {
	return &Client{
		url:      url,
		user:     user,
		password: password,
		http:     &http.Client{},
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse[T any] struct {
	Result T `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) GetBlockCount(ctx context.Context) (uint64, error) {
	var result uint64
	if err := c.call(ctx, "getblockcount", []any{}, &result); err != nil {
		return 0, err
	}
	return result, nil
}

func (c *Client) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	var result string
	if err := c.call(ctx, "getblockhash", []any{height}, &result); err != nil {
		return "", err
	}
	return result, nil
}

func (c *Client) SendRawTransaction(ctx context.Context, rawTx string) (string, error) {
	var result string
	if err := c.call(ctx, "sendrawtransaction", []any{rawTx}, &result); err != nil {
		return "", err
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: "publisher", Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	var decoded rpcResponse[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("rpc %s: %d %s", method, decoded.Error.Code, decoded.Error.Message)
	}
	if err := json.Unmarshal(decoded.Result, out); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}
