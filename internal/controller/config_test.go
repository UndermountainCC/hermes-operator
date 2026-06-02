// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"reflect"
	"testing"
)

func TestParseAllowedClusterRoles(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"cluster-admin", []string{"cluster-admin"}},
		{"cluster-admin,admin,view", []string{"cluster-admin", "admin", "view"}},
		{" cluster-admin , admin ", []string{"cluster-admin", "admin"}},
		{",,,", nil},
	}
	for _, c := range cases {
		got := ParseAllowedClusterRoles(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseAllowedClusterRoles(%q): want %v, got %v", c.in, c.want, got)
		}
	}
}

func TestIsClusterRoleAllowed(t *testing.T) {
	cfg := OperatorConfig{AllowedClusterRoles: []string{"cluster-admin", "view"}}
	if !cfg.IsClusterRoleAllowed("cluster-admin") {
		t.Errorf("cluster-admin should be allowed")
	}
	if cfg.IsClusterRoleAllowed("edit") {
		t.Errorf("edit should NOT be allowed")
	}
}
