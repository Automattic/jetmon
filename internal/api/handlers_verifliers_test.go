package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var (
	quorumVantagesColumns = []string{
		"vantage_id", "region", "provider", "endpoint_host", "endpoint_port",
		"auth_token", "enabled",
	}
	quorumAgentColumns = []string{"vantage_id", "active_agents", "last_seen"}
)

func TestQuorumReportHealthyMix(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(quorumVantagesSQL).
		WillReturnRows(sqlmock.NewRows(quorumVantagesColumns).
			AddRow("v-us-east", "us-east", "aws", "10.0.0.10", "7803", "tok-aaa", 1).
			AddRow("v-us-west", "us-west", "aws", "10.0.1.10", "7803", "tok-bbb", 1).
			AddRow("v-eu", "eu", "gcp", "", "", "", 0))

	now := time.Now().UTC()
	mock.ExpectQuery(quorumActiveAgentsSQL).
		WithArgs(quorumStaleAfterSeconds).
		WillReturnRows(sqlmock.NewRows(quorumAgentColumns).
			AddRow("v-us-east", 2, now.Add(-15*time.Second)).
			AddRow("v-us-west", 1, now.Add(-5*time.Second)))

	req := httptest.NewRequest("GET", "/api/v1/verifliers/quorum-report", nil)
	rec := httptest.NewRecorder()
	s.handleVerifliersQuorumReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body quorumReportResponse
	readJSON(t, rec.Body, &body)
	if body.TotalVantages != 3 {
		t.Errorf("TotalVantages = %d, want 3", body.TotalVantages)
	}
	if body.EnabledCount != 2 {
		t.Errorf("EnabledCount = %d, want 2", body.EnabledCount)
	}
	if body.UsableCount != 2 {
		t.Errorf("UsableCount = %d, want 2 (v-eu has empty endpoint+token)", body.UsableCount)
	}
	if body.HealthyCount != 2 {
		t.Errorf("HealthyCount = %d, want 2", body.HealthyCount)
	}
	if body.StaleAfterSeconds != quorumStaleAfterSeconds {
		t.Errorf("StaleAfterSeconds = %d, want %d", body.StaleAfterSeconds, quorumStaleAfterSeconds)
	}
	// Verify the disabled vantage doesn't leak auth token semantics.
	var disabled *quorumVantageSummary
	for i, v := range body.Vantages {
		if v.VantageID == "v-eu" {
			disabled = &body.Vantages[i]
			break
		}
	}
	if disabled == nil {
		t.Fatal("v-eu missing from response")
	}
	if disabled.AuthTokenPresent {
		t.Errorf("v-eu AuthTokenPresent = true, want false (token is empty)")
	}
	if disabled.Enabled || disabled.Usable || disabled.Healthy {
		t.Errorf("v-eu should be enabled=false usable=false healthy=false; got %+v", disabled)
	}
}

func TestQuorumReportNoHealthyVantages(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(quorumVantagesSQL).
		WillReturnRows(sqlmock.NewRows(quorumVantagesColumns).
			AddRow("v-only", "us-east", "aws", "10.0.0.10", "7803", "tok-aaa", 1))

	// No active agents — vantage is configured but has no heartbeat.
	mock.ExpectQuery(quorumActiveAgentsSQL).
		WithArgs(quorumStaleAfterSeconds).
		WillReturnRows(sqlmock.NewRows(quorumAgentColumns))

	req := httptest.NewRequest("GET", "/api/v1/verifliers/quorum-report", nil)
	rec := httptest.NewRecorder()
	s.handleVerifliersQuorumReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body quorumReportResponse
	readJSON(t, rec.Body, &body)
	if body.HealthyCount != 0 {
		t.Errorf("HealthyCount = %d, want 0 (no agents)", body.HealthyCount)
	}
	if body.Vantages[0].Healthy {
		t.Errorf("vantage Healthy = true with zero ActiveAgents; want false")
	}
}

func TestQuorumReportNeverExposesAuthToken(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(quorumVantagesSQL).
		WillReturnRows(sqlmock.NewRows(quorumVantagesColumns).
			AddRow("v-secret", "us-east", "aws", "10.0.0.10", "7803", "MEGA_SECRET_TOKEN", 1))
	mock.ExpectQuery(quorumActiveAgentsSQL).
		WithArgs(quorumStaleAfterSeconds).
		WillReturnRows(sqlmock.NewRows(quorumAgentColumns))

	req := httptest.NewRequest("GET", "/api/v1/verifliers/quorum-report", nil)
	rec := httptest.NewRecorder()
	s.handleVerifliersQuorumReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Auth token must not appear anywhere in the JSON response.
	if strings.Contains(rec.Body.String(), "MEGA_SECRET_TOKEN") {
		t.Fatalf("response body leaked auth token: %s", rec.Body.String())
	}
}
