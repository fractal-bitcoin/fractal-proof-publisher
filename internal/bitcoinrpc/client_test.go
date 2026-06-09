package bitcoinrpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendRawTransactionErrorIncludesRequest(t *testing.T) {
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":null,"error":{"code":-25,"message":"Unspendable output exceeds maximum configured by user (maxburnamount)"}}`))
	}))
	defer rpcServer.Close()

	client := New(rpcServer.URL, "", "")
	_, err := client.SendRawTransaction(context.Background(), "deadbeef")
	if err == nil {
		t.Fatal("SendRawTransaction() error = nil, want rpc error")
	}
	errorText := err.Error()
	if !strings.Contains(errorText, "request=") {
		t.Fatalf("error = %q, want request body", errorText)
	}
	if !strings.Contains(errorText, `"method":"sendrawtransaction"`) {
		t.Fatalf("error = %q, want sendrawtransaction method", errorText)
	}
	if !strings.Contains(errorText, `"params":["deadbeef",0,1]`) {
		t.Fatalf("error = %q, want raw tx, maxfeerate, and maxburnamount params", errorText)
	}
}
