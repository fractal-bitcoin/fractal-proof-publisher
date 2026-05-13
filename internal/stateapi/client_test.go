package stateapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetHeightStateReturnsExplicitHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing height", http.StatusNotFound)
	}))
	defer server.Close()

	client := New(server.URL, "", time.Second, "")
	_, err := client.GetHeightState(context.Background(), 123)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("expected HTTP status error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing height") {
		t.Fatalf("expected response body in error, got %v", err)
	}
}

func TestGetHeightStateQueryFip101Provider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/brc20/statehash" {
			t.Fatalf("path = %q, want /brc20/statehash", r.URL.Path)
		}
		if r.URL.Query().Get("start") != "123" || r.URL.Query().Get("end") != "123" {
			t.Fatalf("query = %q, want start=end=123", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"detail":[{"blockhash":"aa","statehash":"bb"}]}}`))
	}))
	defer server.Close()

	client := New(server.URL, "", time.Second, "query-fip101")
	state, err := client.GetHeightState(context.Background(), 123)
	if err != nil {
		t.Fatalf("GetHeightState() error = %v", err)
	}
	if state.BlockHash != "aa" || state.StateHash != "bb" {
		t.Fatalf("state = %#v, want blockhash/statehash from query-fip101", state)
	}
}

func TestGetHeightStateQueryFip101ProviderAcceptsCamelCaseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"detail":[{"blockHash":"aa","stateHash":"bb"}]}}`))
	}))
	defer server.Close()

	client := New(server.URL, "", time.Second, "query-fip101")
	state, err := client.GetHeightState(context.Background(), 123)
	if err != nil {
		t.Fatalf("GetHeightState() error = %v", err)
	}
	if state.BlockHash != "aa" || state.StateHash != "bb" {
		t.Fatalf("state = %#v, want blockHash/stateHash from query-fip101", state)
	}
}

func TestGetHeightStateQueryFip101EmptyDetailIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"detail":[]}}`))
	}))
	defer server.Close()

	client := New(server.URL, "", time.Second, "query-fip101")
	_, err := client.GetHeightState(context.Background(), 123)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRetryableHeightUnavailable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestGetHeightStateQueryFip101BehindHeightIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"height":122,"detail":[{"height":123,"blockHash":"aa","stateHash":"bb"}]}}`))
	}))
	defer server.Close()

	client := New(server.URL, "", time.Second, "query-fip101")
	_, err := client.GetHeightState(context.Background(), 123)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRetryableHeightUnavailable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
	if !strings.Contains(err.Error(), "state height 122 is behind requested height 123") {
		t.Fatalf("expected behind height in error, got %v", err)
	}
}

func TestIsRetryableHeightUnavailable(t *testing.T) {
	retryable := []error{
		&StatusError{StatusCode: http.StatusNotFound, URL: "http://example.test/1"},
		&StatusError{StatusCode: http.StatusAccepted, URL: "http://example.test/1"},
		&StatusError{StatusCode: http.StatusTooManyRequests, URL: "http://example.test/1"},
		&StatusError{StatusCode: http.StatusBadGateway, URL: "http://example.test/1"},
	}
	for _, err := range retryable {
		if !IsRetryableHeightUnavailable(err) {
			t.Fatalf("expected retryable for %v", err)
		}
	}

	nonRetryable := []error{
		&StatusError{StatusCode: http.StatusBadRequest, URL: "http://example.test/1"},
		&StatusError{StatusCode: http.StatusForbidden, URL: "http://example.test/1"},
	}
	for _, err := range nonRetryable {
		if IsRetryableHeightUnavailable(err) {
			t.Fatalf("expected non-retryable for %v", err)
		}
	}
}
