package veriflier

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveVeriflierOversizedBatchIsolation(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("JETMON_LIVE_VERIFLIER_ADDR"))
	if addr == "" {
		t.Skip("set JETMON_LIVE_VERIFLIER_ADDR to run against a deployed Veriflier")
	}
	token := strings.TrimSpace(os.Getenv("JETMON_LIVE_VERIFLIER_TOKEN"))
	if token == "" {
		tokenFile := strings.TrimSpace(os.Getenv("JETMON_LIVE_VERIFLIER_TOKEN_FILE"))
		if tokenFile != "" {
			raw, err := os.ReadFile(tokenFile)
			if err != nil {
				t.Fatalf("read token file: %v", err)
			}
			token = strings.TrimSpace(string(raw))
		}
	}
	if token == "" {
		t.Skip("set JETMON_LIVE_VERIFLIER_TOKEN or JETMON_LIVE_VERIFLIER_TOKEN_FILE")
	}

	client := NewVeriflierClient(addr, token)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	results, err := client.CheckBatch(ctx, []CheckRequest{
		{BlogID: 9000000000000001, URL: "https://example.com/", Method: "GET", DetectionProfile: "simple_http", RequestID: "safe-before"},
		{
			BlogID:           9000000000000002,
			URL:              "https://example.com/",
			Method:           "GET",
			DetectionProfile: "simple_http",
			RequestID:        "oversized",
			CustomHeaders: map[string]string{
				"X-Jetmon-Oversized-Payload": strings.Repeat("x", maxRequestBodyBytes+1024),
			},
		},
		{BlogID: 9000000000000003, URL: "https://example.com/", Method: "GET", DetectionProfile: "simple_http", RequestID: "safe-after"},
	})
	if err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	if !results[0].Success || results[0].RequestID != "safe-before" {
		t.Fatalf("safe-before result = %+v, want success for request_id safe-before", results[0])
	}
	if results[1].Success || results[1].RequestID != "oversized" || results[1].Outcome != OutcomeUnknown || results[1].ErrorCode != checkerErrorProbeSafety {
		t.Fatalf("oversized result = %+v, want site-scoped probe-safety unknown", results[1])
	}
	if !results[2].Success || results[2].RequestID != "safe-after" {
		t.Fatalf("safe-after result = %+v, want success for request_id safe-after", results[2])
	}
}
