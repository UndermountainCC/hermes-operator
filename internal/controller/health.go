/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DashboardStatus is the operator's typed view of upstream's /api/status
// response. Schema verified 2026-05-15 against nousresearch/hermes-agent
// v2026.4.30 (hermes_cli/web_server.py:537-640). Only fields relevant to
// the operator are typed; other fields (version, hermes_home, config_path,
// etc.) are ignored on parse.
type DashboardStatus struct {
	GatewayRunning   bool    `json:"gateway_running"`
	GatewayPID       *int    `json:"gateway_pid,omitempty"`
	GatewayHealthURL *string `json:"gateway_health_url,omitempty"`
	// GatewayState: starting | running | draining | stopped | startup_failed.
	// Upstream may also emit "degraded" in future releases (operator maps it
	// to Phase=Degraded if it appears); not present in v2026.4.30 source.
	GatewayState string `json:"gateway_state"`
	// GatewayPlatforms: keyed by platform name (e.g., "discord", "telegram") —
	// same tokens as spec.gateways[].type. Empty when (a) no platforms
	// configured, (b) gateway not running (server clears it), or (c) gateway
	// running but no platform has reported yet. Don't infer Ready=false from
	// empty alone.
	GatewayPlatforms  map[string]PlatformState `json:"gateway_platforms"`
	GatewayExitReason *string                  `json:"gateway_exit_reason,omitempty"`
	GatewayUpdatedAt  string                   `json:"gateway_updated_at,omitempty"`
	ActiveSessions    int                      `json:"active_sessions"`
}

// PlatformState is the per-platform entry in /api/status's gateway_platforms
// map. Verified against gateway/status.py:394-432 and gateway/run.py +
// gateway/platforms/base.py state transitions.
//
// ErrorCode / ErrorMessage are only present when State == "fatal".
// Empty UpdatedAt is tolerated.
type PlatformState struct {
	// State: connecting | connected | disconnected | retrying | fatal.
	State        string `json:"state"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// defaultProbeDashboardStatus calls the dashboard's /api/status endpoint and
// returns the parsed payload. Used by reconcileStatus to populate
// agent.Status.Gateways. Returns an error on transport failure, non-200
// response, or JSON parse failure — caller treats that as "unknown" state
// and leaves the prior status.gateways[] snapshot in place rather than
// wiping it.
func defaultProbeDashboardStatus(ctx context.Context, url string) (*DashboardStatus, error) {
	cli := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call dashboard /api/status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dashboard /api/status returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var out DashboardStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return &out, nil
}
