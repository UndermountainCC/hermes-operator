// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import "strings"

// OperatorConfig carries install-time policy. Populated in cmd/main.go from
// CLI flags / env, passed into the reconciler at SetupWithManager time.
type OperatorConfig struct {
	// AllowedClusterRoles lists the ClusterRole names that may appear in
	// HermesAgent.spec.rbac.clusterRoleBindings[].roleRef.name. Default is
	// empty — no cluster-scoped bindings permitted on any CR. Cluster admin
	// opts in by setting --allowed-cluster-roles=cluster-admin,admin,view.
	AllowedClusterRoles []string
}

// IsClusterRoleAllowed returns true when the named ClusterRole is in the
// allowlist.
func (c OperatorConfig) IsClusterRoleAllowed(name string) bool {
	for _, allowed := range c.AllowedClusterRoles {
		if allowed == name {
			return true
		}
	}
	return false
}

// ParseAllowedClusterRoles converts a comma-separated string into a slice,
// trimming whitespace and dropping empty entries. Accepts "" as "no roles
// allowed" (returns nil).
func ParseAllowedClusterRoles(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
