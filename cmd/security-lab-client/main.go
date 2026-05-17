// security-lab-client is a lab-only helper for repeatable adversarial
// Veriflier checks. It is intentionally outside the production build targets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/veriflier"
)

type scenario struct {
	name           string
	url            string
	timeoutMS      int64
	bodyReadBytes  int64
	bodyReadMS     int32
	wantHTTPStatus int
	wantSuccess    *bool
	wantOutcome    string
	wantErrorCode  *int32
}

type scenarioResult struct {
	Name       string `json:"name"`
	Pass       bool   `json:"pass"`
	DurationMS int64  `json:"duration_ms"`
	HTTPStatus int    `json:"http_status"`
	Outcome    string `json:"outcome,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	ErrorCode  *int32 `json:"error_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7803", "Veriflier host:port")
	token := flag.String("token", "", "Veriflier bearer token")
	targetBase := flag.String("target-base", "", "public HTTP responder base URL")
	tlsBase := flag.String("tls-base", "", "public HTTPS responder base URL")
	flag.Parse()

	if *token == "" || *targetBase == "" {
		fmt.Fprintln(os.Stderr, "-token and -target-base are required")
		os.Exit(2)
	}

	scenarios := buildScenarios(*targetBase, *tlsBase)
	client := &http.Client{Timeout: 10 * time.Second}
	var failed bool
	results := make([]scenarioResult, 0, len(scenarios))
	for _, sc := range scenarios {
		result := runScenario(client, *addr, *token, sc)
		results = append(results, result)
		status := "PASS"
		if !result.Pass {
			status = "FAIL"
			failed = true
		}
		fmt.Printf("%-18s %-4s http=%d duration_ms=%d outcome=%s success=%s error_code=%s error=%s\n",
			sc.name,
			status,
			result.HTTPStatus,
			result.DurationMS,
			result.Outcome,
			boolLabel(result.Success),
			int32Label(result.ErrorCode),
			result.Error,
		)
	}

	encoded, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(encoded))
	if failed {
		os.Exit(1)
	}
}

func buildScenarios(targetBase, tlsBase string) []scenario {
	base := strings.TrimRight(targetBase, "/")
	boolTrue := true
	boolFalse := false
	errNone := int32(checker.ErrorNone)
	errProbeSafety := int32(checker.ErrorProbeSafety)
	errBodyRead := int32(checker.ErrorBodyRead)
	errRedirect := int32(checker.ErrorRedirect)
	errConnect := int32(checker.ErrorConnect)
	errSSL := int32(checker.ErrorSSL)

	scenarios := []scenario{
		{
			name:           "unsafe-loopback",
			url:            "http://127.0.0.1/private",
			timeoutMS:      1000,
			wantHTTPStatus: http.StatusBadRequest,
		},
		{
			name:           "ok-public",
			url:            base + "/ok",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolTrue,
			wantOutcome:    veriflier.OutcomeUp,
			wantErrorCode:  &errNone,
		},
		{
			name:           "redirect-local",
			url:            base + "/redirect-local",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeUnknown,
			wantErrorCode:  &errProbeSafety,
		},
		{
			name:           "redirect-metadata",
			url:            base + "/redirect-metadata",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeUnknown,
			wantErrorCode:  &errProbeSafety,
		},
		{
			name:           "redirect-loop",
			url:            base + "/redirect-loop",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeProbeError,
			wantErrorCode:  &errRedirect,
		},
		{
			name:           "infinite-body",
			url:            base + "/infinite",
			timeoutMS:      3000,
			bodyReadBytes:  1024,
			bodyReadMS:     1000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolTrue,
			wantOutcome:    veriflier.OutcomeUp,
			wantErrorCode:  &errNone,
		},
		{
			name:           "slow-body",
			url:            base + "/slow-body",
			timeoutMS:      5000,
			bodyReadBytes:  1024,
			bodyReadMS:     50,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeProbeError,
			wantErrorCode:  &errBodyRead,
		},
		{
			name:           "huge-header",
			url:            base + "/huge-header",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeProbeError,
			wantErrorCode:  &errConnect,
		},
		{
			name:           "gzip-bomb",
			url:            base + "/gzip-bomb",
			timeoutMS:      3000,
			bodyReadBytes:  1024,
			bodyReadMS:     1000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolTrue,
			wantOutcome:    veriflier.OutcomeUp,
			wantErrorCode:  &errNone,
		},
	}

	if tlsBase != "" {
		scenarios = append(scenarios, scenario{
			name:           "tls-self-signed",
			url:            strings.TrimRight(tlsBase, "/") + "/ok",
			timeoutMS:      3000,
			wantHTTPStatus: http.StatusOK,
			wantSuccess:    &boolFalse,
			wantOutcome:    veriflier.OutcomeProbeError,
			wantErrorCode:  &errSSL,
		})
	}
	return scenarios
}

func runScenario(client *http.Client, addr, token string, sc scenario) scenarioResult {
	start := time.Now()
	status, result, err := postCheck(client, addr, token, sc)
	out := scenarioResult{
		Name:       sc.name,
		DurationMS: time.Since(start).Milliseconds(),
		HTTPStatus: status,
	}
	if result != nil {
		out.Outcome = result.Outcome
		out.Success = &result.Success
		out.ErrorCode = &result.ErrorCode
	}
	if err != nil {
		out.Error = err.Error()
	}
	out.Pass = scenarioPassed(sc, status, result, err)
	return out
}

func postCheck(client *http.Client, addr, token string, sc scenario) (int, *veriflier.CheckV2Result, error) {
	reqBody := veriflier.CheckV2BatchRequest{
		BatchID:    sc.name,
		DeadlineMS: sc.timeoutMS + 500,
		Requests: []veriflier.CheckV2Request{{
			RequestID:        sc.name,
			BlogID:           1001,
			URL:              sc.url,
			TimeoutMS:        sc.timeoutMS,
			BodyReadMaxBytes: sc.bodyReadBytes,
			BodyReadMaxMS:    sc.bodyReadMS,
		}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v2/check", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, nil, errors.New(strings.TrimSpace(string(body)))
	}
	var decoded veriflier.CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return resp.StatusCode, nil, err
	}
	if len(decoded.Results) != 1 {
		return resp.StatusCode, nil, fmt.Errorf("got %d results, want 1", len(decoded.Results))
	}
	return resp.StatusCode, &decoded.Results[0], nil
}

func scenarioPassed(sc scenario, status int, result *veriflier.CheckV2Result, err error) bool {
	if status != sc.wantHTTPStatus {
		return false
	}
	if sc.wantHTTPStatus != http.StatusOK {
		return status == sc.wantHTTPStatus
	}
	if err != nil || result == nil {
		return false
	}
	if sc.wantSuccess != nil && result.Success != *sc.wantSuccess {
		return false
	}
	if sc.wantOutcome != "" && result.Outcome != sc.wantOutcome {
		return false
	}
	if sc.wantErrorCode != nil && result.ErrorCode != *sc.wantErrorCode {
		return false
	}
	return true
}

func boolLabel(v *bool) string {
	if v == nil {
		return "-"
	}
	if *v {
		return "true"
	}
	return "false"
}

func int32Label(v *int32) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}
