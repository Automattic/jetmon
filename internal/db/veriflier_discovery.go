package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const VeriflierDiscoveryDefaultStaleAfter = 90 * time.Second

type VeriflierVantage struct {
	VantageID    string
	Region       string
	Provider     string
	EndpointHost string
	EndpointPort string
	AuthToken    string
	Enabled      bool
	LastSeen     *time.Time
	ActiveAgents int
}

func (v VeriflierVantage) Usable() bool {
	return strings.TrimSpace(v.EndpointHost) != "" &&
		strings.TrimSpace(v.EndpointPort) != "" &&
		strings.TrimSpace(v.AuthToken) != ""
}

type VeriflierAgentHeartbeat struct {
	AgentID        string
	VantageID      string
	Hostname       string
	EndpointHost   string
	EndpointPort   string
	Version        string
	Protocols      []string
	MaxConcurrency int
	QueueCapacity  int
	QueueDepth     int
	Active         int
	InFlight       int
	Status         string
}

type VeriflierAgent struct {
	AgentID        string
	VantageID      string
	Hostname       string
	EndpointHost   string
	EndpointPort   string
	Version        string
	Protocols      []string
	MaxConcurrency int
	QueueCapacity  int
	QueueDepth     int
	Active         int
	InFlight       int
	Status         string
	LastSeen       time.Time
}

type VeriflierDiscoverySnapshot struct {
	Vantages []VeriflierVantage
	Agents   []VeriflierAgent
}

func UpsertVeriflierAgent(ctx context.Context, hb VeriflierAgentHeartbeat) error {
	hb.AgentID = strings.TrimSpace(hb.AgentID)
	hb.VantageID = strings.TrimSpace(hb.VantageID)
	if hb.AgentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	if hb.VantageID == "" {
		return fmt.Errorf("vantage_id is required")
	}
	status := strings.TrimSpace(hb.Status)
	if status == "" {
		status = "active"
	}
	protocols, err := json.Marshal(hb.Protocols)
	if err != nil {
		return fmt.Errorf("marshal protocols: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO jetpack_monitor_veriflier_agents (
			agent_id, vantage_id, hostname, endpoint_host, endpoint_port,
			version, protocols, max_concurrency, queue_capacity, queue_depth,
			active, in_flight, status, last_seen
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP())
		ON DUPLICATE KEY UPDATE
			vantage_id = VALUES(vantage_id),
			hostname = VALUES(hostname),
			endpoint_host = VALUES(endpoint_host),
			endpoint_port = VALUES(endpoint_port),
			version = VALUES(version),
			protocols = VALUES(protocols),
			max_concurrency = VALUES(max_concurrency),
			queue_capacity = VALUES(queue_capacity),
			queue_depth = VALUES(queue_depth),
			active = VALUES(active),
			in_flight = VALUES(in_flight),
			status = VALUES(status),
			last_seen = UTC_TIMESTAMP(),
			updated_at = UTC_TIMESTAMP()`,
		hb.AgentID,
		hb.VantageID,
		strings.TrimSpace(hb.Hostname),
		strings.TrimSpace(hb.EndpointHost),
		strings.TrimSpace(hb.EndpointPort),
		strings.TrimSpace(hb.Version),
		string(protocols),
		clampNonNegative(hb.MaxConcurrency),
		clampNonNegative(hb.QueueCapacity),
		clampNonNegative(hb.QueueDepth),
		clampNonNegative(hb.Active),
		clampNonNegative(hb.InFlight),
		status,
	)
	if err != nil {
		return fmt.Errorf("upsert veriflier agent: %w", err)
	}
	return nil
}

func MarkVeriflierAgentStopped(ctx context.Context, agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_veriflier_agents
		    SET status = 'stopped',
		        last_seen = UTC_TIMESTAMP(),
		        updated_at = UTC_TIMESTAMP()
		  WHERE agent_id = ?`,
		agentID,
	)
	if err != nil {
		return fmt.Errorf("mark veriflier agent stopped: %w", err)
	}
	return nil
}

func ListEnabledVeriflierVantages(ctx context.Context, staleAfter time.Duration) ([]VeriflierVantage, error) {
	vantages, err := listVeriflierVantages(ctx, true)
	if err != nil {
		return nil, err
	}
	agents, err := ListRecentVeriflierAgents(ctx, staleAfter)
	if err != nil {
		return nil, err
	}
	applyAgentHints(vantages, agents)
	return vantages, nil
}

func ListVeriflierDiscoverySnapshot(ctx context.Context, staleAfter time.Duration) (VeriflierDiscoverySnapshot, error) {
	vantages, err := listVeriflierVantages(ctx, false)
	if err != nil {
		return VeriflierDiscoverySnapshot{}, err
	}
	agents, err := ListRecentVeriflierAgents(ctx, staleAfter)
	if err != nil {
		return VeriflierDiscoverySnapshot{}, err
	}
	applyAgentHints(vantages, agents)
	return VeriflierDiscoverySnapshot{Vantages: vantages, Agents: agents}, nil
}

func ListRecentVeriflierAgents(ctx context.Context, staleAfter time.Duration) ([]VeriflierAgent, error) {
	seconds := staleAfterSeconds(staleAfter)
	rows, err := ReadDB().QueryContext(ctx, `
		SELECT agent_id, vantage_id, hostname, endpoint_host, endpoint_port,
		       version, protocols, max_concurrency, queue_capacity, queue_depth,
		       active, in_flight, status, last_seen
		  FROM jetpack_monitor_veriflier_agents
		 WHERE last_seen >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL ? SECOND)
		 ORDER BY vantage_id, last_seen DESC, agent_id`,
		seconds,
	)
	if err != nil {
		return nil, fmt.Errorf("list veriflier agents: %w", err)
	}
	defer rows.Close()

	var agents []VeriflierAgent
	for rows.Next() {
		agent, err := scanVeriflierAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list veriflier agents: %w", err)
	}
	return agents, nil
}

func listVeriflierVantages(ctx context.Context, enabledOnly bool) ([]VeriflierVantage, error) {
	query := `
		SELECT vantage_id, region, provider, endpoint_host, endpoint_port,
		       auth_token, enabled
		  FROM jetpack_monitor_veriflier_vantages`
	if enabledOnly {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY vantage_id`

	rows, err := ReadDB().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list veriflier vantages: %w", err)
	}
	defer rows.Close()

	var vantages []VeriflierVantage
	for rows.Next() {
		var v VeriflierVantage
		var enabled int
		if err := rows.Scan(&v.VantageID, &v.Region, &v.Provider, &v.EndpointHost, &v.EndpointPort, &v.AuthToken, &enabled); err != nil {
			return nil, fmt.Errorf("scan veriflier vantage: %w", err)
		}
		v.Enabled = enabled != 0
		vantages = append(vantages, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list veriflier vantages: %w", err)
	}
	return vantages, nil
}

type agentScanner interface {
	Scan(dest ...any) error
}

func scanVeriflierAgent(row agentScanner) (VeriflierAgent, error) {
	var agent VeriflierAgent
	var protocols sql.NullString
	if err := row.Scan(
		&agent.AgentID,
		&agent.VantageID,
		&agent.Hostname,
		&agent.EndpointHost,
		&agent.EndpointPort,
		&agent.Version,
		&protocols,
		&agent.MaxConcurrency,
		&agent.QueueCapacity,
		&agent.QueueDepth,
		&agent.Active,
		&agent.InFlight,
		&agent.Status,
		&agent.LastSeen,
	); err != nil {
		return VeriflierAgent{}, fmt.Errorf("scan veriflier agent: %w", err)
	}
	if protocols.Valid && strings.TrimSpace(protocols.String) != "" {
		if err := json.Unmarshal([]byte(protocols.String), &agent.Protocols); err != nil {
			return VeriflierAgent{}, fmt.Errorf("decode veriflier agent protocols: %w", err)
		}
	}
	return agent, nil
}

func applyAgentHints(vantages []VeriflierVantage, agents []VeriflierAgent) {
	byID := make(map[string]int, len(vantages))
	for i := range vantages {
		byID[vantages[i].VantageID] = i
	}
	for _, agent := range agents {
		i, ok := byID[agent.VantageID]
		if !ok || agent.Status != "active" {
			continue
		}
		v := &vantages[i]
		v.ActiveAgents++
		if v.LastSeen == nil || agent.LastSeen.After(*v.LastSeen) {
			lastSeen := agent.LastSeen
			v.LastSeen = &lastSeen
		}
		if strings.TrimSpace(v.EndpointHost) == "" && strings.TrimSpace(agent.EndpointHost) != "" {
			v.EndpointHost = agent.EndpointHost
		}
		if strings.TrimSpace(v.EndpointPort) == "" && strings.TrimSpace(agent.EndpointPort) != "" {
			v.EndpointPort = agent.EndpointPort
		}
	}
}

func staleAfterSeconds(d time.Duration) int {
	if d <= 0 {
		d = VeriflierDiscoveryDefaultStaleAfter
	}
	seconds := int(d.Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
