// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"
)

func TestIsKindContext(t *testing.T) {
	cases := []struct {
		name    string
		context string
		want    bool
	}{
		{"kind cluster full name", "kind-hermes-test", true},
		{"kind cluster minimal", "kind-", true},
		{"kind- prefix only", "kind-foo-bar-baz", true},
		{"default context", "default", false},
		{"empty string", "", false},
		{"minikube", "minikube", false},
		{"docker-desktop", "docker-desktop", false},
		{"eks context", "arn:aws:eks:us-east-1:123456789012:cluster/my-cluster", false},
		{"gke context", "gke_my-project_us-central1_my-cluster", false},
		{"aks context", "my-aks-cluster", false},
		{"orbstack k8s", "orbstack", false},
		{"prod context named kind-alike", "kinder-cluster", false}, // "kinder" doesn't start with "kind-"
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isKindContext(tc.context)
			if got != tc.want {
				t.Errorf("isKindContext(%q) = %v, want %v", tc.context, got, tc.want)
			}
		})
	}
}
