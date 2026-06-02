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

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"

	"github.com/UndermountainCC/hermes-operator/test/utils"
)

// testNamespace is the namespace where HermesAgent CRs land during E2E tests.
// Created lazily by ensureTestNamespace.
const testNamespace = "hermes-e2e"

// ensureTestNamespace creates the test namespace if it doesn't already exist.
// Safe to call repeatedly — uses kubectl apply for idempotency.
func ensureTestNamespace() {
	cmd := exec.Command("kubectl", "create", "ns", testNamespace)
	_, err := utils.Run(cmd)
	// "already exists" is fine
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		Expect(err).NotTo(HaveOccurred())
	}
}

// applyManifest applies a YAML manifest via kubectl. Returns the kubectl output
// (or stderr on failure) so callers can assert against rejection messages from
// the admission webhook.
func applyManifest(manifest string) (string, error) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// deleteAgentCR removes a HermesAgent by name+namespace, ignoring not-found errors.
func deleteAgentCR(name, ns string) {
	cmd := exec.Command("kubectl", "-n", ns, "delete", "hermesagent", name, "--ignore-not-found=true")
	_, _ = utils.Run(cmd)
}

// getAgentJSON returns the full HermesAgent JSON as a map[string]interface{}.
// Use when you need to traverse nested fields like .status.gateways[].state.
//
//nolint:unused // helper kept for future specs that need full CR traversal
func getAgentJSON(name, ns string) (map[string]interface{}, error) {
	cmd := exec.Command("kubectl", "-n", ns, "get", "hermesagent", name, "-o", "json")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return m, nil
}

// getAgentField returns the value at jsonpath from a HermesAgent. Wraps
// kubectl's jsonpath; useful for simple polls like waiting on status.phase.
func getAgentField(name, ns, jsonpath string) string {
	cmd := exec.Command("kubectl", "-n", ns, "get", "hermesagent", name, "-o",
		fmt.Sprintf("jsonpath=%s", jsonpath))
	out, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// getDeploymentJSON returns the operator-generated agent Deployment as
// map[string]interface{}. Deployment is named hermes-<agentName>.
func getDeploymentJSON(agentName, ns string) (map[string]interface{}, error) {
	cmd := exec.Command("kubectl", "-n", ns, "get", "deployment",
		fmt.Sprintf("hermes-%s", agentName), "-o", "json")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// curlInCluster runs an ephemeral curl pod against a target URL and returns
// the response body. Used to hit Services from inside the cluster (e.g.,
// the dashboard's /api/status).
//
// Idempotent: deletes any pre-existing curl pod with the same name first.
func curlInCluster(ns, podName, url string) (string, error) {
	_ = exec.Command("kubectl", "-n", ns, "delete", "pod", podName, "--ignore-not-found=true").Run()
	cmd := exec.Command("kubectl", "-n", ns, "run", podName,
		"--image=curlimages/curl",
		"--restart=Never",
		"--rm", "-i", "--quiet",
		"--", "curl", "-sS", url)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
