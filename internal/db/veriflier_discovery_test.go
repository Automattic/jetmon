package db

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpsertVeriflierAgentValidatesIdentity(t *testing.T) {
	if err := UpsertVeriflierAgent(context.Background(), VeriflierAgentHeartbeat{}); err == nil {
		t.Fatal("UpsertVeriflierAgent accepted empty agent id")
	}
	if err := UpsertVeriflierAgent(context.Background(), VeriflierAgentHeartbeat{AgentID: "agent"}); err == nil {
		t.Fatal("UpsertVeriflierAgent accepted empty vantage id")
	}
}

func TestUpsertVeriflierAgentWritesHeartbeat(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectExec("INSERT INTO jetpack_monitor_veriflier_agents").
		WithArgs(
			"agent-1", "us-east", "host-a", "veriflier-a", "7803",
			"test-version", `["v2-json-http"]`, 32, 64, 3, 2, 1, "active",
		).
		WillReturnResult(driver.RowsAffected(1))

	err := UpsertVeriflierAgent(context.Background(), VeriflierAgentHeartbeat{
		AgentID:        " agent-1 ",
		VantageID:      " us-east ",
		Hostname:       "host-a",
		EndpointHost:   "veriflier-a",
		EndpointPort:   "7803",
		Version:        "test-version",
		Protocols:      []string{"v2-json-http"},
		MaxConcurrency: 32,
		QueueCapacity:  64,
		QueueDepth:     3,
		Active:         2,
		InFlight:       1,
	})
	if err != nil {
		t.Fatalf("UpsertVeriflierAgent: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMarkVeriflierAgentStopped(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectExec("UPDATE jetpack_monitor_veriflier_agents").
		WithArgs("agent-1").
		WillReturnResult(driver.RowsAffected(1))

	if err := MarkVeriflierAgentStopped(context.Background(), "agent-1"); err != nil {
		t.Fatalf("MarkVeriflierAgentStopped: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListEnabledVeriflierVantagesAppliesActiveAgentHints(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	vantageRows := sqlmock.NewRows([]string{
		"vantage_id", "region", "provider", "endpoint_host", "endpoint_port", "auth_token", "enabled",
	}).
		AddRow("us-east", "iad", "provider-a", "", "", "token-east", 1).
		AddRow("us-west", "sfo", "provider-b", "west.example", "7803", "token-west", 1)
	agentRows := sqlmock.NewRows([]string{
		"agent_id", "vantage_id", "hostname", "endpoint_host", "endpoint_port",
		"version", "protocols", "max_concurrency", "queue_capacity", "queue_depth",
		"active", "in_flight", "status", "last_seen",
	}).
		AddRow("agent-east", "us-east", "host-east", "east.example", "7804",
			"dev", `["v2-json-http"]`, 64, 256, 4, 2, 1, "active", time.Now()).
		AddRow("agent-stopped", "us-west", "host-west", "ignored.example", "7999",
			"dev", `["v2-json-http"]`, 64, 256, 0, 0, 0, "stopped", time.Now())

	mock.ExpectQuery("SELECT vantage_id").
		WillReturnRows(vantageRows)
	mock.ExpectQuery("SELECT agent_id").
		WithArgs(90).
		WillReturnRows(agentRows)

	got, err := ListEnabledVeriflierVantages(context.Background(), VeriflierDiscoveryDefaultStaleAfter)
	if err != nil {
		t.Fatalf("ListEnabledVeriflierVantages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].EndpointHost != "east.example" || got[0].EndpointPort != "7804" || got[0].ActiveAgents != 1 || !got[0].Usable() {
		t.Fatalf("east vantage = %+v", got[0])
	}
	if got[1].EndpointHost != "west.example" || got[1].EndpointPort != "7803" || got[1].ActiveAgents != 0 || !got[1].Usable() {
		t.Fatalf("west vantage = %+v", got[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListVeriflierDiscoverySnapshotIncludesDisabledVantages(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	vantageRows := sqlmock.NewRows([]string{
		"vantage_id", "region", "provider", "endpoint_host", "endpoint_port", "auth_token", "enabled",
	}).
		AddRow("disabled", "dev", "local", "disabled.example", "7803", "token", 0)
	agentRows := sqlmock.NewRows([]string{
		"agent_id", "vantage_id", "hostname", "endpoint_host", "endpoint_port",
		"version", "protocols", "max_concurrency", "queue_capacity", "queue_depth",
		"active", "in_flight", "status", "last_seen",
	}).
		AddRow("agent-disabled", "disabled", "host", "disabled.example", "7803",
			"dev", `["legacy-json-http","v2-json-http"]`, 8, 32, 0, 0, 0, "active", time.Now())

	mock.ExpectQuery("SELECT vantage_id").
		WillReturnRows(vantageRows)
	mock.ExpectQuery("SELECT agent_id").
		WithArgs(90).
		WillReturnRows(agentRows)

	got, err := ListVeriflierDiscoverySnapshot(context.Background(), VeriflierDiscoveryDefaultStaleAfter)
	if err != nil {
		t.Fatalf("ListVeriflierDiscoverySnapshot: %v", err)
	}
	if len(got.Vantages) != 1 || got.Vantages[0].Enabled {
		t.Fatalf("vantages = %+v", got.Vantages)
	}
	if len(got.Agents) != 1 || strings.Join(got.Agents[0].Protocols, ",") != "legacy-json-http,v2-json-http" {
		t.Fatalf("agents = %+v", got.Agents)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
