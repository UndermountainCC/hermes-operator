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
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/UndermountainCC/hermes-operator/test/utils"
)

// Cross-PR integration test for the Kubernetes exec backend.
//
// What this proves: when a HermesAgent CR sets spec.execBackend=kubernetes,
// the operator (a) provisions the per-agent session SA + scoped Role +
// RoleBinding via exec_rbac.go, AND (b) stamps the three env vars the agent's
// terminal_tool.py reads to select the kubernetes backend and address the
// operator-provisioned SA / namespace.
//
// What this does NOT prove: the agent's actual session-pod-creation logic
// works. That's covered by the agent's own kubernetes-environment tests
// (UndermountainCC/hermes-agent: tests/tools/test_kubernetes_environment.py).
// This spec is the *contract* test for the operator-side half of the bridge.
//
// Compatibility: passes against any hermes-agent image whose env-honoring
// surface matches the contract (TERMINAL_ENV + TERMINAL_KUBERNETES_POD_SA +
// TERMINAL_KUBERNETES_NAMESPACE). Override the image via HERMES_AGENT_IMAGE
// for cross-PR validation:
//
//	HERMES_AGENT_IMAGE=docker.io/nousresearch/hermes-agent:v2026.4.30 \
//	  make test-e2e
var _ = Describe("Kubernetes exec backend integration (Phase 10)", Ordered, func() {
	const agentName = "exec-backend-k8s"

	BeforeAll(func() {
		ensureTestNamespace()
	})

	AfterAll(func() {
		deleteAgentCR(agentName, testNamespace)
		// Operator's ownerRef GC removes the SA/Role/RoleBinding when the CR
		// goes away, but Roles/RoleBindings are cluster-scope-ish via RBAC
		// caching; explicit delete is belt-and-suspenders.
		_ = exec.Command("kubectl", "-n", testNamespace, "delete", "sa",
			fmt.Sprintf("hermes-%s-session", agentName),
			"--ignore-not-found=true").Run()
		_ = exec.Command("kubectl", "-n", testNamespace, "delete", "role",
			fmt.Sprintf("hermes-%s-exec", agentName),
			"--ignore-not-found=true").Run()
		_ = exec.Command("kubectl", "-n", testNamespace, "delete", "rolebinding",
			fmt.Sprintf("hermes-%s-exec", agentName),
			"--ignore-not-found=true").Run()
	})

	It("provisions RBAC + stamps TERMINAL_* env when spec.execBackend=kubernetes", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: %s
  execBackend: kubernetes
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - { name: DEEPSEEK_API_KEY, value: "sk-deepseek-placeholder" }
`, agentName, testNamespace, hermesAgentImage)

		By("applying the HermesAgent CR")
		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), "kubectl apply failed: %s", out)

		By("waiting for ExecBackendReady condition to flip True")
		// Operator sets this condition only after exec_rbac.go reconciles
		// the Role + RoleBinding + session SA successfully.
		Eventually(func() string {
			return getAgentField(agentName, testNamespace,
				`{.status.conditions[?(@.type=="ExecBackendReady")].status}`)
		}, "60s", "2s").Should(Equal("True"))

		By("asserting the session ServiceAccount exists with automountServiceAccountToken=false")
		saOut, err := utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "sa", fmt.Sprintf("hermes-%s-session", agentName),
			"-o", "jsonpath={.automountServiceAccountToken}"))
		Expect(err).NotTo(HaveOccurred(), "session SA missing")
		// jsonpath returns string "false" — verify the SA is not automounting.
		Expect(strings.TrimSpace(saOut)).To(Equal("false"),
			"session SA must have automountServiceAccountToken=false (powerless identity)")

		By("asserting the exec Role exists")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "role", fmt.Sprintf("hermes-%s-exec", agentName)))
		Expect(err).NotTo(HaveOccurred(), "exec Role missing")

		By("asserting the exec RoleBinding exists")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "rolebinding", fmt.Sprintf("hermes-%s-exec", agentName)))
		Expect(err).NotTo(HaveOccurred(), "exec RoleBinding missing")

		By("verifying TERMINAL_* env vars are stamped on the Deployment spec")
		// Query the Deployment's container env directly — that's what the
		// operator produces. Doing it this way (vs `kubectl exec` into the
		// pod) keeps the test orthogonal to pod readiness, which depends on
		// the agent image accepting the placeholder API key.

		// TERMINAL_ENV: plain value, expect "kubernetes".
		envOut, err := utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "deployment", fmt.Sprintf("hermes-%s", agentName),
			"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="TERMINAL_ENV")].value}`))
		Expect(err).NotTo(HaveOccurred(), "deployment query for TERMINAL_ENV failed")
		Expect(strings.TrimSpace(envOut)).To(Equal("kubernetes"),
			"operator must stamp TERMINAL_ENV=kubernetes when spec.execBackend=kubernetes")

		// TERMINAL_KUBERNETES_POD_SA: plain value, expect the session SA name.
		envOut, err = utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "deployment", fmt.Sprintf("hermes-%s", agentName),
			"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="TERMINAL_KUBERNETES_POD_SA")].value}`))
		Expect(err).NotTo(HaveOccurred(), "deployment query for TERMINAL_KUBERNETES_POD_SA failed")
		Expect(strings.TrimSpace(envOut)).To(Equal(fmt.Sprintf("hermes-%s-session", agentName)),
			"operator must stamp TERMINAL_KUBERNETES_POD_SA matching execSessionSAName")

		// TERMINAL_KUBERNETES_NAMESPACE: Downward API ref to metadata.namespace.
		// Query the valueFrom.fieldRef.fieldPath rather than .value because
		// Downward API entries have nil .value at the Deployment level.
		envOut, err = utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"get", "deployment", fmt.Sprintf("hermes-%s", agentName),
			"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="TERMINAL_KUBERNETES_NAMESPACE")].valueFrom.fieldRef.fieldPath}`))
		Expect(err).NotTo(HaveOccurred(), "deployment query for TERMINAL_KUBERNETES_NAMESPACE failed")
		Expect(strings.TrimSpace(envOut)).To(Equal("metadata.namespace"),
			"operator must stamp TERMINAL_KUBERNETES_NAMESPACE via Downward API to metadata.namespace")
	})

	It("clears RBAC + TERMINAL_* env when spec.execBackend toggles to local", func() {
		By("patching the CR to set execBackend=local")
		_, err := utils.Run(exec.Command("kubectl", "-n", testNamespace,
			"patch", "hermesagent", agentName, "--type=merge",
			"-p", `{"spec":{"execBackend":"local"}}`))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the ExecBackendReady condition to be removed")
		// hasExecBackendReadyCondition returns false → exec_rbac.go enters
		// the GC branch and removes the SA/Role/RoleBinding + clears status.
		Eventually(func() string {
			return getAgentField(agentName, testNamespace,
				`{.status.conditions[?(@.type=="ExecBackendReady")].status}`)
		}, "60s", "2s").Should(BeEmpty(),
			"ExecBackendReady condition must clear when backend toggles off")

		By("asserting the session SA was GC'd")
		Eventually(func() error {
			_, e := utils.Run(exec.Command("kubectl", "-n", testNamespace,
				"get", "sa", fmt.Sprintf("hermes-%s-session", agentName)))
			return e
		}, "30s", "2s").Should(HaveOccurred(),
			"session SA must be GC'd when backend toggles off")

		By("verifying TERMINAL_ENV is no longer in the Deployment spec")
		// The operator's reconciler updates the Deployment spec on every
		// reconcile. Once the condition clears, the next reconcile will
		// drop the TERMINAL_* entries from the env list. Poll until the
		// jsonpath returns empty (env entry absent).
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "-n", testNamespace,
				"get", "deployment", fmt.Sprintf("hermes-%s", agentName),
				"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="TERMINAL_ENV")].value}`))
			return strings.TrimSpace(out)
		}, "60s", "2s").Should(BeEmpty(),
			"TERMINAL_ENV must be removed from Deployment env when execBackend != kubernetes")
	})
})
