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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/UndermountainCC/hermes-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "hermes-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "hermes-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "hermes-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "hermes-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Suite-level BeforeSuite installs CRDs + deploys the operator into
	// hermes-operator-system. The Manager Describe only manages spec-local
	// resources (the curl-metrics pod).
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			// Idempotent on purpose: this cluster-scoped binding is also created
			// by the Phase 9 metrics Describe, and Ginkgo randomizes Describe
			// order. A bare `kubectl create` here races that one and fails with
			// "already exists" whenever Phase 9 runs first — a seed-dependent
			// E2E flake. ensureMetricsRoleBinding swallows the AlreadyExists.
			ensureMetricsRoleBinding()

			By("validating that the metrics service is available")
			cmd := exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		It("does NOT install a ValidatingWebhookConfiguration (Phase 11)", func() {
			By("confirming admission validation lives in the CRD, not a webhook")
			cmd := exec.Command("kubectl", "get",
				"validatingwebhookconfigurations.admissionregistration.k8s.io",
				"hermes-operator-validating-webhook-configuration")
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "ValidatingWebhookConfiguration should no longer exist after Phase 11")
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput := getMetricsOutput()
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})
})

var _ = Describe("HermesAgent lifecycle", Ordered, func() {
	BeforeAll(func() {
		ensureTestNamespace()
		By("creating fake credentials Secret for HermesAgent tests")
		cmd := exec.Command("kubectl", "-n", testNamespace, "create", "secret", "generic", "lifecycle-secrets",
			"--from-literal=DEEPSEEK_API_KEY=fake",
			"--from-literal=DISCORD_BOT_TOKEN=fake")
		_, _ = utils.Run(cmd) // ignore "already exists"
	})

	AfterAll(func() {
		// Best-effort cleanup of CRs left by failed specs.
		cmd := exec.Command("kubectl", "-n", testNamespace, "delete", "hermesagent", "--all", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "-n", testNamespace, "delete", "secret", "lifecycle-secrets", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	It("applies a valid CR, reconciles to Provisioning, children exist", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: lifecycle-basic, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom: { secretKeyRef: { name: lifecycle-secrets, key: DEEPSEEK_API_KEY } }
  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom: { secretKeyRef: { name: lifecycle-secrets, key: DISCORD_BOT_TOKEN } }
`, testNamespace)
		DeferCleanup(func() { deleteAgentCR("lifecycle-basic", testNamespace) })

		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		By("waiting for status.phase to be Provisioning or Ready")
		Eventually(func() string {
			return getAgentField("lifecycle-basic", testNamespace, "{.status.phase}")
		}).Should(BeElementOf("Provisioning", "Ready"))

		By("verifying child resources exist")
		Eventually(func() error {
			_, err := getDeploymentJSON("lifecycle-basic", testNamespace)
			return err
		}).Should(Succeed())

		cmd := exec.Command("kubectl", "-n", testNamespace, "get", "pvc", "hermes-lifecycle-basic-data")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "PVC should exist")

		cmd = exec.Command("kubectl", "-n", testNamespace, "get", "sa", "hermes-lifecycle-basic")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "ServiceAccount should exist")
	})

	It("retains PVC on delete (default RetainPolicy=Retain)", func() {
		// Note: This spec used to also verify ClusterRoleBinding cleanup on agent
		// delete, but the operator's default --allowed-cluster-roles allowlist is
		// empty, so the kustomize `make deploy` install rejects all CRB grants.
		// CRB allowlist + cleanup-on-delete is exercised by envtest in
		// internal/controller. The e2e value-add here is PVC retention across
		// the agent's lifecycle delete event, which envtest can't realistically
		// reproduce.
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: lifecycle-delete, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
`, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		By("waiting for the PVC to be created")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "pvc",
				"hermes-lifecycle-delete-data")
			_, err := utils.Run(cmd)
			return err
		}).Should(Succeed())

		By("deleting the HermesAgent CR")
		deleteAgentCR("lifecycle-delete", testNamespace)

		By("waiting for the CR to be fully deleted")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "hermesagent",
				"lifecycle-delete")
			_, err := utils.Run(cmd)
			return err
		}).ShouldNot(Succeed())

		By("PVC should be retained per default RetainPolicy=Retain")
		cmd := exec.Command("kubectl", "-n", testNamespace, "get", "pvc", "hermes-lifecycle-delete-data")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "PVC must still exist after CR delete")
		// Best-effort cleanup so we don't leak PVCs across runs.
		cmd = exec.Command("kubectl", "-n", testNamespace, "delete", "pvc",
			"hermes-lifecycle-delete-data", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})
})

// Phase 11 replaced the validating admission webhook with CRD-level
// validation: OpenAPI schema (MinLength) + x-kubernetes-validations CEL
// rules. The API server reports CRDV failures with the CEL `message:`
// verbatim; the field-path on the cause may be the struct path rather than
// the leaf, so the assertions below match on the message text where the
// previous webhook version matched on the field path.
var _ = Describe("CRD validation (CRDV)", Ordered, func() {
	BeforeAll(func() {
		ensureTestNamespace()
	})

	It("rejects CRs with empty spec.image", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: crdv-empty-image, namespace: %s }
spec:
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
`, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred(), "CRDV should reject")
		Expect(out).To(ContainSubstring("spec.image"))
	})

	It("rejects CRs with unknown llmDefaultProvider", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: crdv-unknown-provider, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: anthropic
`, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("must match a name in spec.llmProviders"))
	})

	It("rejects CRs with empty gateways[].type", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: crdv-empty-gateway-type, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  gateways:
    - {}
`, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred())
		// CRD schema MinLength=1 on gateways[].type — message includes the field path.
		Expect(out).To(ContainSubstring("gateways"))
	})

	It("rejects CRs with ingress.enabled but no host/ingressClassName", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: crdv-bad-ingress, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  dashboard:
    enabled: true
    ingress:
      enabled: true
`, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("dashboard.ingress.host is required when dashboard.ingress.enabled is true"))
		Expect(out).To(ContainSubstring("dashboard.ingress.ingressClassName is required when dashboard.ingress.enabled is true"))
	})
})

var _ = Describe("Self-introspection RBAC (Phase 10.6)", Ordered, func() {
	const agentName = "self-rbac"

	BeforeAll(func() {
		ensureTestNamespace()
	})

	AfterAll(func() {
		deleteAgentCR(agentName, testNamespace)
		cmd := exec.Command("kubectl", "-n", testNamespace, "delete", "pvc",
			fmt.Sprintf("hermes-%s-data", agentName), "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	It("creates a per-agent self Role + RoleBinding scoped to the agent's own names", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
`, agentName, testNamespace)

		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		roleName := fmt.Sprintf("hermes-%s-self", agentName)

		By("waiting for the self Role to be reconciled")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "role", roleName)
			_, err := utils.Run(cmd)
			return err
		}).Should(Succeed())

		By("waiting for the self RoleBinding to be reconciled")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "rolebinding", roleName)
			_, err := utils.Run(cmd)
			return err
		}).Should(Succeed())

		By("verifying the agent SA can patch its OWN Deployment (rollout restart)")
		// kube-apiserver's RBAC authz cache can lag the Role/RoleBinding API
		// create by a few seconds — wrap the positive can-i in Eventually so
		// we don't false-positive a propagation race.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "-n", testNamespace, "auth", "can-i", "patch",
				fmt.Sprintf("deployment/hermes-%s", agentName),
				"--as", fmt.Sprintf("system:serviceaccount:%s:hermes-%s", testNamespace, agentName))
			ans, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), ans)
			g.Expect(strings.TrimSpace(ans)).To(Equal("yes"))
		}).Should(Succeed())

		By("verifying the agent SA CANNOT patch a Deployment with a different name")
		// `auth can-i` for a denied verb exits non-zero with stdout "no"; we
		// just assert the answer rather than the exit status.
		cmd := exec.Command("kubectl", "-n", testNamespace, "auth", "can-i", "patch",
			"deployment/some-other-deployment",
			"--as", fmt.Sprintf("system:serviceaccount:%s:hermes-%s", testNamespace, agentName))
		raw, _ := cmd.CombinedOutput()
		Expect(strings.TrimSpace(string(raw))).To(Equal("no"))

		By("verifying the agent SA CANNOT delete its own Deployment")
		cmd = exec.Command("kubectl", "-n", testNamespace, "auth", "can-i", "delete",
			fmt.Sprintf("deployment/hermes-%s", agentName),
			"--as", fmt.Sprintf("system:serviceaccount:%s:hermes-%s", testNamespace, agentName))
		raw, _ = cmd.CombinedOutput()
		Expect(strings.TrimSpace(string(raw))).To(Equal("no"))

		By("verifying the agent SA CANNOT list pods (no namespace-wide access)")
		cmd = exec.Command("kubectl", "-n", testNamespace, "auth", "can-i", "list", "pods",
			"--as", fmt.Sprintf("system:serviceaccount:%s:hermes-%s", testNamespace, agentName))
		raw, _ = cmd.CombinedOutput()
		Expect(strings.TrimSpace(string(raw))).To(Equal("no"))
	})
})

var _ = Describe("Pod invariants + exec probe", Ordered, func() {
	BeforeAll(func() {
		ensureTestNamespace()
		cmd := exec.Command("kubectl", "-n", testNamespace, "create", "secret", "generic", "probe-secrets",
			"--from-literal=DEEPSEEK_API_KEY=fake")
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		// The probe-invariants CR is shared across all three specs in this Describe,
		// so cleanup must be Describe-wide (AfterAll) — not per-spec via DeferCleanup,
		// which would tear down the agent between specs.
		deleteAgentCR("probe-invariants", testNamespace)
		cmd := exec.Command("kubectl", "-n", testNamespace, "delete", "secret", "probe-secrets", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	It("Deployment has replicas=1 and strategy=Recreate", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: probe-invariants, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom: { secretKeyRef: { name: probe-secrets, key: DEEPSEEK_API_KEY } }
`, testNamespace)

		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		Eventually(func() string {
			return getAgentField("probe-invariants", testNamespace, "{.status.phase}")
		}).Should(BeElementOf("Provisioning", "Ready"))

		dep, err := getDeploymentJSON("probe-invariants", testNamespace)
		Expect(err).NotTo(HaveOccurred())

		spec := dep["spec"].(map[string]interface{})
		Expect(spec["replicas"]).To(BeNumerically("==", 1))
		strategy := spec["strategy"].(map[string]interface{})
		Expect(strategy["type"]).To(Equal("Recreate"))
	})

	It("container has exec readiness+liveness probes targeting hermes gateway status", func() {
		dep, err := getDeploymentJSON("probe-invariants", testNamespace)
		Expect(err).NotTo(HaveOccurred())
		podSpec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		containers := podSpec["containers"].([]interface{})
		Expect(containers).NotTo(BeEmpty())
		c := containers[0].(map[string]interface{})

		readiness := c["readinessProbe"].(map[string]interface{})
		execProbe := readiness["exec"].(map[string]interface{})
		cmd := execProbe["command"].([]interface{})
		// Expect ["/bin/bash", "-c", "/opt/hermes/.venv/bin/hermes gateway status | grep -q '✓ Gateway is running'"]
		Expect(cmd).To(HaveLen(3))
		Expect(cmd[0]).To(Equal("/bin/bash"))
		Expect(cmd[1]).To(Equal("-c"))
		Expect(cmd[2].(string)).To(ContainSubstring("hermes gateway status"))
		Expect(cmd[2].(string)).To(ContainSubstring("Gateway is running"))

		liveness := c["livenessProbe"].(map[string]interface{})
		Expect(liveness["exec"]).NotTo(BeNil(), "liveness must also use exec")
	})

	It("status.gateways[] stays empty without dashboard sidecar", func() {
		// After reconciliation has settled, gateways should remain unpopulated.
		Eventually(func() string {
			return getAgentField("probe-invariants", testNamespace, "{.status.gateways}")
		}).Should(BeEmpty())
	})
})

var _ = Describe("Dashboard sidecar (Phase 7b)", Ordered, func() {
	BeforeAll(func() {
		ensureTestNamespace()
		cmd := exec.Command("kubectl", "-n", testNamespace, "create", "secret", "generic", "dash-secrets",
			"--from-literal=DEEPSEEK_API_KEY=fake",
			"--from-literal=DISCORD_BOT_TOKEN=fake")
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		cmd := exec.Command("kubectl", "-n", testNamespace, "delete", "hermesagent", "dash-test", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "-n", testNamespace, "delete", "secret", "dash-secrets", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	const agentName = "dash-test"

	It("creates two containers, shareProcessNamespace=true, dashboard Service", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom: { secretKeyRef: { name: dash-secrets, key: DEEPSEEK_API_KEY } }
  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom: { secretKeyRef: { name: dash-secrets, key: DISCORD_BOT_TOKEN } }
  dashboard:
    enabled: true
`, agentName, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		Eventually(func() bool {
			dep, err := getDeploymentJSON(agentName, testNamespace)
			if err != nil {
				return false
			}
			spec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
			shared, ok := spec["shareProcessNamespace"].(bool)
			if !ok || !shared {
				return false
			}
			containers := spec["containers"].([]interface{})
			return len(containers) == 2
		}).Should(BeTrue())

		By("dashboard Service exists on port 9119")
		cmd := exec.Command("kubectl", "-n", testNamespace, "get", "svc",
			fmt.Sprintf("hermes-%s-dashboard", agentName), "-o", "jsonpath={.spec.ports[0].port}")
		out2, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out2)).To(Equal("9119"))
	})

	It("dashboard's /api/status is reachable via Service and reports gateway_running=true", func() {
		// Give the sidecar time to come up. Hermes-agent image is ~2.5GB so the
		// first pull on a cold cluster can take several minutes. Bumped to 5m to
		// tolerate cold pulls + crash-loop restarts (Discord auth fails with the
		// fake token, but the dashboard sidecar itself reaches ready quickly).
		Eventually(func() bool {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "pod",
				"-l", fmt.Sprintf("hermes.undermountain.cc/agent=%s", agentName),
				"-o", "jsonpath={.items[0].status.containerStatuses[*].ready}")
			out, err := utils.Run(cmd)
			if err != nil {
				return false
			}
			// At least one container (the dashboard) should be ready.
			return strings.Contains(out, "true")
		}, "5m", "5s").Should(BeTrue())

		// /api/status is unauth and should serve even before the gateway is up.
		var body string
		Eventually(func() error {
			var err error
			body, err = curlInCluster(testNamespace, "curl-dash-status",
				fmt.Sprintf("http://hermes-%s-dashboard.%s.svc:9119/api/status", agentName, testNamespace))
			return err
		}, "60s", "5s").Should(Succeed(), body)
		Expect(body).To(ContainSubstring("gateway_state"))

		// shareProcessNamespace lets upstream's PID-based liveness work
		// cross-container — dashboard should see gateway as running for at least
		// one poll cycle. Because Discord auth crashloops with the fake token,
		// `gateway_running:true` only appears during the brief alive window;
		// retry up to 2m. If still flaky in CI, asserting that `gateway_state`
		// is *populated* (a non-empty value other than `"unknown"`) is the
		// fallback — that's enough to prove the dashboard saw the PID.
		Eventually(func() (string, error) {
			b, err := curlInCluster(testNamespace, "curl-dash-status-loop",
				fmt.Sprintf("http://hermes-%s-dashboard.%s.svc:9119/api/status", agentName, testNamespace))
			return b, err
		}, "2m", "5s").Should(ContainSubstring(`"gateway_running":true`))
	})

	It("status.gateways[] populates from dashboard probe", func() {
		Eventually(func() string {
			return getAgentField(agentName, testNamespace, "{.status.gateways[0].type}")
		}, "90s", "5s").Should(Equal("discord"))
	})

	It("Ingress reconciliation passes annotations through when ingress.enabled", func() {
		cmd := exec.Command("kubectl", "-n", testNamespace, "patch", "hermesagent", agentName,
			"--type=merge", "-p", `{"spec":{"dashboard":{"ingress":{"enabled":true,"host":"dash.example.com","ingressClassName":"nginx","annotations":{"nginx.ingress.kubernetes.io/auth-url":"https://auth.example.com/verify"}}}}}`)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "ingress",
				fmt.Sprintf("hermes-%s-dashboard", agentName),
				"-o", `jsonpath={.metadata.annotations.nginx\.ingress\.kubernetes\.io/auth-url}`)
			out, _ := utils.Run(cmd)
			return strings.TrimSpace(out)
		}).Should(Equal("https://auth.example.com/verify"))
	})

	It("disabling dashboard removes sidecar, Service, and Ingress", func() {
		cmd := exec.Command("kubectl", "-n", testNamespace, "patch", "hermesagent", agentName,
			"--type=merge", "-p", `{"spec":{"dashboard":{"enabled":false}}}`)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			dep, err := getDeploymentJSON(agentName, testNamespace)
			if err != nil {
				return false
			}
			spec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
			if _, ok := spec["shareProcessNamespace"]; ok {
				// field present means still on
				return false
			}
			containers := spec["containers"].([]interface{})
			return len(containers) == 1
		}).Should(BeTrue())

		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "svc",
				fmt.Sprintf("hermes-%s-dashboard", agentName))
			_, err := utils.Run(cmd)
			return err
		}).ShouldNot(Succeed())

		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "ingress",
				fmt.Sprintf("hermes-%s-dashboard", agentName))
			_, err := utils.Run(cmd)
			return err
		}).ShouldNot(Succeed())
	})
})

// Phase 9: NetworkPolicy reconciliation.
//
// Default kind clusters use kindnet, which DOES NOT enforce NetworkPolicy
// resources — they're created but ignored by the data plane. This spec
// therefore verifies operator-side behavior only: the resource is created
// with the correct shape when spec.networkPolicy.enabled, deleted on
// toggle-off, and pod selector targets the agent's labels.
//
// Real enforcement testing requires a CNI that implements NetworkPolicy
// (Calico, Cilium). Out of scope for the operator's own E2E — that's
// integration-level testing of the cluster, not the controller.
var _ = Describe("NetworkPolicy reconciliation (Phase 9)", Ordered, func() {
	const agentName = "netpol-test"

	BeforeAll(func() {
		ensureTestNamespace()
	})

	AfterAll(func() {
		deleteAgentCR(agentName, testNamespace)
	})

	It("creates a NetworkPolicy when spec.networkPolicy.enabled, deletes on toggle-off", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  networkPolicy:
    enabled: true
    policyTypes: [Ingress]
    ingress:
      - from:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: hermes-operator-system
`, agentName, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		By("verifying the NetworkPolicy is created")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "networkpolicy",
				fmt.Sprintf("hermes-%s", agentName))
			_, err := utils.Run(cmd)
			return err
		}, "30s", "2s").Should(Succeed())

		By("verifying pod selector matches the agent labels")
		cmd := exec.Command("kubectl", "-n", testNamespace, "get", "networkpolicy",
			fmt.Sprintf("hermes-%s", agentName),
			"-o", "jsonpath={.spec.podSelector.matchLabels.hermes\\.undermountain\\.cc/agent}")
		selector, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(selector)).To(Equal(agentName))

		By("verifying ingress rules pass through unchanged")
		cmd = exec.Command("kubectl", "-n", testNamespace, "get", "networkpolicy",
			fmt.Sprintf("hermes-%s", agentName),
			"-o", "jsonpath={.spec.ingress[0].from[0].namespaceSelector.matchLabels.kubernetes\\.io/metadata\\.name}")
		nsLabel, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(nsLabel)).To(Equal("hermes-operator-system"))

		By("toggling spec.networkPolicy.enabled=false")
		cmd = exec.Command("kubectl", "-n", testNamespace, "patch", "hermesagent", agentName,
			"--type=merge", "-p", `{"spec":{"networkPolicy":{"enabled":false}}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the NetworkPolicy is deleted (operator in-place cleanup, not ownerRef GC)")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "-n", testNamespace, "get", "networkpolicy",
				fmt.Sprintf("hermes-%s", agentName))
			_, err := utils.Run(cmd)
			return err
		}, "30s", "2s").ShouldNot(Succeed())
	})

	It("CRDV rejects networkPolicy.enabled with no rules (Phase 11 promoted warning → hard error)", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: netpol-deny-all, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  networkPolicy:
    enabled: true
`, testNamespace)
		out, err := applyManifest(manifest)
		// Phase 11: this used to be a warning; now it's a hard reject via CRDV.
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("deny-all policy"))
	})
})

// Phase 9: custom Prometheus metrics. Verifies the operator's existing
// /metrics endpoint surfaces the new hermes_agent_* gauges via the same
// curl-from-cluster pattern the Manager describe block uses.
var _ = Describe("HermesAgent custom metrics (Phase 9)", Ordered, func() {
	const agentName = "metrics-test"

	BeforeAll(func() {
		ensureTestNamespace()
		cmd := exec.Command("kubectl", "-n", testNamespace, "create", "secret", "generic", "metrics-secrets",
			"--from-literal=DEEPSEEK_API_KEY=fake")
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		deleteAgentCR(agentName, testNamespace)
		cmd := exec.Command("kubectl", "-n", testNamespace, "delete", "secret", "metrics-secrets", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics-phase9", "-n", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	It("exposes hermes_agent_phase + hermes_agent_pod_ready for a live CR", func() {
		By("applying a basic HermesAgent CR")
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom: { secretKeyRef: { name: metrics-secrets, key: DEEPSEEK_API_KEY } }
`, agentName, testNamespace)
		out, err := applyManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), out)

		By("waiting for the operator to reconcile + write status (gates phase metric)")
		Eventually(func() string {
			return getAgentField(agentName, testNamespace, "{.status.phase}")
		}, "60s", "2s").ShouldNot(BeEmpty())

		By("scraping the operator's /metrics endpoint via a curl-from-cluster pod")
		// Reuse the existing token + ClusterRoleBinding from the Manager
		// Describe block's metrics test. If that test hasn't run in this
		// invocation (Describe ordering isn't guaranteed across runs), set
		// up a fresh binding here.
		ensureMetricsRoleBinding()
		token, err := serviceAccountToken()
		Expect(err).NotTo(HaveOccurred())
		Expect(token).NotTo(BeEmpty())

		cmd := exec.Command("kubectl", "run", "curl-metrics-phase9", "--restart=Never",
			"--namespace", namespace,
			"--image=curlimages/curl:7.87.0",
			"--overrides",
			fmt.Sprintf(`{
				"spec": {
					"containers": [{
						"name": "curl",
						"image": "curlimages/curl:7.87.0",
						"command": ["/bin/sh", "-c", "curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
						"securityContext": {
							"allowPrivilegeEscalation": false,
							"capabilities": {"drop": ["ALL"]},
							"runAsNonRoot": true,
							"runAsUser": 1000,
							"seccompProfile": {"type": "RuntimeDefault"}
						}
					}],
					"serviceAccount": "%s",
					"terminationGracePeriodSeconds": 0
				}
			}`, token, metricsServiceName, namespace, serviceAccountName))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "failed to create curl-metrics-phase9 pod")

		By("waiting for the curl pod to succeed")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "curl-metrics-phase9",
				"-o", "jsonpath={.status.phase}", "-n", namespace)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Succeeded"))
		}, "2m").Should(Succeed())

		By("scraping curl logs for hermes_agent_phase metric")
		cmd = exec.Command("kubectl", "logs", "curl-metrics-phase9", "-n", namespace)
		metricsOutput, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(metricsOutput).To(ContainSubstring("hermes_agent_phase"))
		Expect(metricsOutput).To(ContainSubstring(fmt.Sprintf("name=\"%s\"", agentName)))
		Expect(metricsOutput).To(ContainSubstring("hermes_agent_pod_ready"))
	})
})

// ensureMetricsRoleBinding sets up the ClusterRoleBinding the curl-metrics
// pod needs to scrape the operator's HTTPS metrics endpoint. Idempotent:
// safe to call when the binding already exists (the Manager Describe creates
// it but ordering across Describes isn't guaranteed).
func ensureMetricsRoleBinding() {
	cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
		"--clusterrole=hermes-operator-metrics-reader",
		"--serviceaccount="+namespace+":"+serviceAccountName)
	_, _ = utils.Run(cmd) // ignore "already exists"
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
