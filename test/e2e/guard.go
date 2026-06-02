// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"
)

// isKindContext reports whether the kubectl context name looks like a kind
// cluster (i.e. starts with "kind-"). This is a pure string predicate —
// testable without a live cluster.
func isKindContext(name string) bool {
	return strings.HasPrefix(name, "kind-")
}

// currentKubectlContext returns the name of the active kubectl context.
func currentKubectlContext() (string, error) {
	out, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil {
		return "", fmt.Errorf("kubectl config current-context: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// requireKindContext calls Gomega Fail if the current kubectl context is not a
// kind cluster. Call this at the very top of BeforeSuite, before any make
// install / make deploy step. The e2e suite runs make install + make uninstall
// which installs and deletes the CRD — on a production cluster that
// cascade-deletes every HermesAgent CR and triggers the operator to GC all
// agent Deployments.
func requireKindContext() {
	ctx, err := currentKubectlContext()
	Expect(err).NotTo(HaveOccurred(),
		"REFUSING to run e2e: could not determine kubectl context (is kubectl configured?)")

	Expect(isKindContext(ctx)).To(BeTrue(),
		fmt.Sprintf(
			"REFUSING to run e2e: current kubectl context %q is not a kind cluster "+
				"(expected a name starting with \"kind-\"). "+
				"The e2e suite runs make install / make uninstall which installs and "+
				"DELETES the CRD — on a real cluster that cascade-deletes every "+
				"HermesAgent CR and the operator then GCs all agent Deployments. "+
				"Switch to a kind cluster before running the e2e suite.",
			ctx,
		))
}
