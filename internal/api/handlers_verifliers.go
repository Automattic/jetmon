package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// quorumReportResponse summarizes Veriflier vantage health from the operator's
// perspective. Operators use this to decide whether enough vantages are alive
// to support the configured detection quorum, and which vantage to investigate
// when a vote becomes unbalanced. Auth tokens are deliberately omitted — the
// auth_token_present boolean is the only thing this surface should reveal.
type quorumReportResponse struct {
	GeneratedAt       string                 `json:"generated_at"`
	StaleAfterSeconds int                    `json:"stale_after_seconds"`
	TotalVantages     int                    `json:"total_vantages"`
	EnabledCount      int                    `json:"enabled_count"`
	UsableCount       int                    `json:"usable_count"`
	HealthyCount      int                    `json:"healthy_count"`
	Vantages          []quorumVantageSummary `json:"vantages"`
}

type quorumVantageSummary struct {
	VantageID        string `json:"vantage_id"`
	Region           string `json:"region,omitempty"`
	Provider         string `json:"provider,omitempty"`
	EndpointHost     string `json:"endpoint_host,omitempty"`
	EndpointPort     string `json:"endpoint_port,omitempty"`
	AuthTokenPresent bool   `json:"auth_token_present"`
	Enabled          bool   `json:"enabled"`
	Usable           bool   `json:"usable"`
	Healthy          bool   `json:"healthy"`
	ActiveAgents     int    `json:"active_agents"`
	LastSeen         string `json:"last_seen,omitempty"`
	LastSeenAgeSec   int64  `json:"last_seen_age_sec,omitempty"`
}

// quorumStaleAfterSeconds is the heartbeat freshness threshold for counting
// an agent as "active" against a vantage. Matches
// db.VeriflierDiscoveryDefaultStaleAfter (90s).
const quorumStaleAfterSeconds = 90

const quorumVantagesSQL = `
		SELECT vantage_id, region, provider, endpoint_host, endpoint_port,
		       auth_token, enabled
		  FROM jetmon_veriflier_vantages
		 ORDER BY vantage_id`

const quorumActiveAgentsSQL = `
		SELECT vantage_id, COUNT(*) AS active_agents, MAX(last_seen) AS last_seen
		  FROM jetmon_veriflier_agents
		 WHERE last_seen >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL ? SECOND)
		   AND status = 'active'
		 GROUP BY vantage_id`

// handleVerifliersQuorumReport reports current vantage health: how many
// enabled vantages exist, how many are usable (have host+port+token), and how
// many have reported a heartbeat recently enough to be counted toward
// detection quorum. Operators consult this when alerting fires "all verifiers
// in operational cooldown" or when a detection seems to wait on too few
// votes.
//
// The handler runs two focused queries against the server's injected DB
// handle rather than going through db.ListVeriflierDiscoverySnapshot so that
// sqlmock-backed unit tests can drive the result set directly.
func (s *Server) handleVerifliersQuorumReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	vantages, err := s.queryQuorumVantages(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "quorum_report_lookup_failed",
			"reading veriflier vantages failed: "+err.Error())
		return
	}
	agentStats, err := s.queryQuorumActiveAgents(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "quorum_report_lookup_failed",
			"reading veriflier agent heartbeats failed: "+err.Error())
		return
	}

	now := time.Now().UTC()
	resp := quorumReportResponse{
		GeneratedAt:       now.Format(time.RFC3339),
		StaleAfterSeconds: quorumStaleAfterSeconds,
		Vantages:          make([]quorumVantageSummary, 0, len(vantages)),
	}

	for _, v := range vantages {
		summary := v
		if stats, ok := agentStats[summary.VantageID]; ok {
			summary.ActiveAgents = stats.ActiveAgents
			if !stats.LastSeen.IsZero() {
				summary.LastSeen = stats.LastSeen.Format(time.RFC3339)
				if age := now.Sub(stats.LastSeen); age >= 0 {
					summary.LastSeenAgeSec = int64(age.Seconds())
				}
			}
		}
		summary.Healthy = summary.Enabled && summary.Usable && summary.ActiveAgents > 0
		resp.TotalVantages++
		if summary.Enabled {
			resp.EnabledCount++
		}
		if summary.Usable {
			resp.UsableCount++
		}
		if summary.Healthy {
			resp.HealthyCount++
		}
		resp.Vantages = append(resp.Vantages, summary)
	}

	writeJSON(w, http.StatusOK, resp)
}

type quorumAgentStats struct {
	ActiveAgents int
	LastSeen     time.Time
}

func (s *Server) queryQuorumVantages(ctx context.Context) ([]quorumVantageSummary, error) {
	rows, err := s.db.QueryContext(ctx, quorumVantagesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quorumVantageSummary
	for rows.Next() {
		var (
			summary   quorumVantageSummary
			authToken string
			enabled   int
		)
		if err := rows.Scan(
			&summary.VantageID, &summary.Region, &summary.Provider,
			&summary.EndpointHost, &summary.EndpointPort,
			&authToken, &enabled,
		); err != nil {
			return nil, err
		}
		summary.AuthTokenPresent = strings.TrimSpace(authToken) != ""
		summary.Enabled = enabled != 0
		summary.Usable = strings.TrimSpace(summary.EndpointHost) != "" &&
			strings.TrimSpace(summary.EndpointPort) != "" &&
			summary.AuthTokenPresent
		out = append(out, summary)
	}
	return out, rows.Err()
}

func (s *Server) queryQuorumActiveAgents(ctx context.Context) (map[string]quorumAgentStats, error) {
	rows, err := s.db.QueryContext(ctx, quorumActiveAgentsSQL, quorumStaleAfterSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]quorumAgentStats)
	for rows.Next() {
		var (
			vantageID string
			active    int
			lastSeen  sql.NullTime
		)
		if err := rows.Scan(&vantageID, &active, &lastSeen); err != nil {
			return nil, err
		}
		stats := quorumAgentStats{ActiveAgents: active}
		if lastSeen.Valid {
			stats.LastSeen = lastSeen.Time.UTC()
		}
		out[vantageID] = stats
	}
	return out, rows.Err()
}
