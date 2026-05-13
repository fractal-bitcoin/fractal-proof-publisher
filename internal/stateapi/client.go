package stateapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL  string
	auth     string
	http     *http.Client
	provider string
}

type HeightState struct {
	BlockHash string `json:"blockhash"`
	StateHash string `json:"statehash"`
}

func (s *HeightState) UnmarshalJSON(data []byte) error {
	var raw struct {
		BlockHashLower string `json:"blockhash"`
		StateHashLower string `json:"statehash"`
		BlockHashCamel string `json:"blockHash"`
		StateHashCamel string `json:"stateHash"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	s.BlockHash = firstNonEmpty(raw.BlockHashLower, raw.BlockHashCamel)
	s.StateHash = firstNonEmpty(raw.StateHashLower, raw.StateHashCamel)
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type StatusError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("unexpected status %d from %s", e.StatusCode, e.URL)
	}
	return fmt.Sprintf("unexpected status %d from %s: %s", e.StatusCode, e.URL, body)
}

type queryFip101Envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Height uint64        `json:"height"`
		Detail []HeightState `json:"detail"`
	} `json:"data"`
}

func New(baseURL, auth string, timeout time.Duration, provider string) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		auth:     auth,
		http:     &http.Client{Timeout: timeout},
		provider: strings.ToLower(strings.TrimSpace(provider)),
	}
}

func IsRetryableHeightUnavailable(err error) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		return false
	}

	switch statusErr.StatusCode {
	case http.StatusNotFound, http.StatusAccepted, http.StatusNoContent, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return statusErr.StatusCode >= 500
}

func (c *Client) GetHeightState(ctx context.Context, height uint64) (HeightState, error) {
	url := c.baseURL + "/" + strconv.FormatUint(height, 10)
	if c.provider == "query-fip101" {
		url = fmt.Sprintf("%s/brc20/statehash?start=%d&end=%d", c.baseURL, height, height)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HeightState{}, fmt.Errorf("new request: %w", err)
	}
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return HeightState{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return HeightState{}, &StatusError{
			StatusCode: resp.StatusCode,
			URL:        url,
			Body:       string(body),
		}
	}

	if c.provider == "query-fip101" {
		var envelope queryFip101Envelope
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return HeightState{}, fmt.Errorf("decode query-fip101 response: %w", err)
		}
		if envelope.Code != 0 {
			msg := strings.TrimSpace(envelope.Msg)
			if msg == "" {
				msg = "query-fip101 returned non-zero code"
			}
			return HeightState{}, fmt.Errorf("%s", msg)
		}
		if len(envelope.Data.Detail) == 0 {
			return HeightState{}, &StatusError{
				StatusCode: http.StatusNotFound,
				URL:        url,
				Body:       "query-fip101 returned empty detail",
			}
		}
		if envelope.Data.Height != 0 && envelope.Data.Height < height {
			return HeightState{}, &StatusError{
				StatusCode: http.StatusTooEarly,
				URL:        url,
				Body:       fmt.Sprintf("query-fip101 state height %d is behind requested height %d", envelope.Data.Height, height),
			}
		}
		return envelope.Data.Detail[0], nil
	}

	var state HeightState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return HeightState{}, fmt.Errorf("decode response: %w", err)
	}
	return state, nil
}
