package feeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

type RecommendedFees struct {
	FastestFee  int64 `json:"fastestFee"`
	HalfHourFee int64 `json:"halfHourFee"`
	HourFee     int64 `json:"hourFee"`
	MinimumFee  int64 `json:"minimumFee"`
}

func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *Client) RecommendedFees(ctx context.Context) (RecommendedFees, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/fees/recommended", nil)
	if err != nil {
		return RecommendedFees{}, fmt.Errorf("new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return RecommendedFees{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	var fees RecommendedFees
	if err := json.NewDecoder(resp.Body).Decode(&fees); err != nil {
		return RecommendedFees{}, fmt.Errorf("decode response: %w", err)
	}
	return fees, nil
}
