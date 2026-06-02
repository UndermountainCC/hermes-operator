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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/UndermountainCC/hermes-operator/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	projectImage = "example.com/hermes-operator:v0.0.1"

	// hermesAgentImage is the upstream Hermes agent image referenced by all
	// HermesAgent CR specs. Pre-pulled into Kind in BeforeSuite so HermesAgent
	// pods don't pay a ~2.5GB cold pull during the suite.
	// Override via HERMES_AGENT_IMAGE env var to test against a locally-built
	// image (e.g. a feature-branch PR build). When overridden, the docker pull
	// is skipped — the image is expected to already exist in the local daemon.
	hermesAgentImage = func() string {
		if v := os.Getenv("HERMES_AGENT_IMAGE"); v != "" {
			return v
		}
		return "docker.io/nousresearch/hermes-agent:v2026.4.30"
	}()
	hermesAgentImageIsLocal = os.Getenv("HERMES_AGENT_IMAGE") != ""
)

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting hermes-operator integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	// Safety guard: refuse to run against any non-kind cluster.
	// make install / make uninstall (below) installs and DELETES the CRD.
	// On a real cluster that cascade-deletes every HermesAgent CR and the
	// operator GCs all agent Deployments. Running against the wrong context
	// has caused production incidents. This check must stay at the top of
	// BeforeSuite, before any cluster-mutating step.
	By("verifying kubectl context is a kind cluster (prod-context guard)")
	requireKindContext()

	By("building the manager(Operator) image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager(Operator) image")

	// TODO(user): If you want to change the e2e test vendor from Kind, ensure the image is
	// built and available before running the tests. Also, remove the following block.
	//
	// `kind load docker-image` can hit a transient containerd snapshotter glitch
	// ("ctr: content digest sha256:... not found") when docker's layer cache and
	// the kind node disagree mid-load. Retry a few times with generous spacing
	// so kind has room to settle; surface the last error verbatim on final fail.
	By("loading the manager(Operator) image on Kind")
	var lastLoadErr error
	Eventually(func() error {
		lastLoadErr = utils.LoadImageToKindClusterWithName(projectImage)
		return lastLoadErr
	}, "3m", "30s").Should(Succeed(),
		fmt.Sprintf("Failed to load the manager(Operator) image into Kind after retries: %v", lastLoadErr))

	// Pre-pull the hermes-agent image and load it into Kind so HermesAgent CR
	// specs don't time out waiting on a ~2.5GB cold pull from docker.io. Order
	// matters: `docker pull` populates the daemon's layer cache that `kind load`
	// reads from — skipping the pull means `kind load` can race a partial cache
	// (failure mode A). The CI workflow caches this image tarball via
	// actions/cache; locally each cold machine pays it once. Pre-load failure
	// here is non-fatal — specs will fall back to pulling on-demand, just
	// slower — but we still retry the kind-load step a few times to survive
	// containerd snapshotter glitches before giving up.
	By("pre-pulling hermes-agent image and loading into Kind")
	// When HERMES_AGENT_IMAGE is set, the caller has already built/tagged the
	// image locally; pulling would either fail (no registry copy) or shadow
	// the local build with a registry version.
	if hermesAgentImageIsLocal {
		_, _ = fmt.Fprintf(GinkgoWriter,
			"skipping docker pull — HERMES_AGENT_IMAGE=%s assumed local\n",
			hermesAgentImage)
	} else {
		cmd = exec.Command("docker", "pull", hermesAgentImage)
		_, pullErr := utils.Run(cmd)
		if pullErr != nil {
			_, _ = fmt.Fprintf(GinkgoWriter,
				"docker pull failed: %v — kind load will be skipped\n", pullErr)
			return
		}
	}
	{
		const hermesLoadAttempts = 3
		var hermesLoadErr error
		for attempt := 1; attempt <= hermesLoadAttempts; attempt++ {
			hermesLoadErr = utils.LoadImageToKindClusterWithName(hermesAgentImage)
			if hermesLoadErr == nil {
				break
			}
			_, _ = fmt.Fprintf(GinkgoWriter,
				"kind-load hermes-agent attempt %d/%d failed: %s\n",
				attempt, hermesLoadAttempts, hermesLoadErr)
			if attempt < hermesLoadAttempts {
				time.Sleep(30 * time.Second)
			}
		}
		if hermesLoadErr != nil {
			_, _ = fmt.Fprintf(GinkgoWriter,
				"WARNING: failed to kind-load hermes-agent image after %d attempts (will pull on-demand): %s\n",
				hermesLoadAttempts, hermesLoadErr)
		}
	}

	// The tests-e2e are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	// Install CRDs + deploy the operator once per suite so every Describe
	// (Manager, HermesAgent lifecycle, Admission webhook, Pod invariants,
	// Dashboard sidecar) shares one operator install. Previously this lived
	// in Describe("Manager").BeforeAll and was torn down before the new
	// Phase 8 Describes ran, which caused 4xx NotFound on CR applies.
	// Clean up cluster-scoped leftovers from any prior interrupted run on
	// the same Kind cluster (CRBs survive namespace teardown).
	By("cleaning up cluster-scoped leftovers from prior runs")
	_ = exec.Command("kubectl", "delete", "clusterrolebinding",
		"hermes-operator-metrics-binding", "--ignore-not-found=true").Run()

	By("creating manager namespace")
	cmd = exec.Command("kubectl", "create", "ns", "hermes-operator-system")
	_, err = utils.Run(cmd)
	if err != nil && strings.Contains(err.Error(), "AlreadyExists") {
		err = nil
	}
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create namespace")

	By("labeling the namespace to enforce the restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", "hermes-operator-system",
		"pod-security.kubernetes.io/enforce=restricted")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

	By("installing CRDs")
	cmd = exec.Command("make", "install")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install CRDs")

	By("deploying the controller-manager")
	// `make deploy` can race cert-manager's webhook readiness: cert-manager-webhook's
	// Deployment.condition=Available flips before the webhook's TLS bundle is injected,
	// so the first `kubectl apply` of our webhook resources can fail with
	// "x509: certificate signed by unknown authority". Retry a handful of times.
	Eventually(func() error {
		cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err := utils.Run(cmd)
		return err
	}, "3m", "10s").Should(Succeed(), "Failed to deploy the controller-manager")

	By("waiting for the controller-manager deployment to be Available")
	cmd = exec.Command("kubectl", "-n", "hermes-operator-system", "wait",
		"deployment/hermes-operator-controller-manager",
		"--for=condition=Available", "--timeout=3m")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "controller-manager deployment never became Available")

	By("waiting for the validating webhook endpoint to accept connections")
	// `kubectl apply` of the ValidatingWebhookConfiguration races the operator pod
	// reaching readiness — the webhook Service has no endpoints until the pod's
	// :9443 listener is up. Poke it with a no-op CR apply until it stops returning
	// "connection refused".
	Eventually(func() error {
		probe := `apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: webhook-readiness-probe, namespace: default }
spec: {}
`
		c := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
		c.Stdin = strings.NewReader(probe)
		out, err := c.CombinedOutput()
		// We expect the webhook to *reject* the empty-spec CR — that means it's reachable.
		// "connection refused" / "no endpoints available" / context deadline are not-ready signals.
		if err == nil {
			return nil
		}
		s := string(out)
		if strings.Contains(s, "connection refused") ||
			strings.Contains(s, "no endpoints available") ||
			strings.Contains(s, "context deadline exceeded") ||
			strings.Contains(s, "service unavailable") {
			return fmt.Errorf("webhook not ready: %s", strings.TrimSpace(s))
		}
		// Any other error (e.g. webhook denied / validation failed) means the webhook is up.
		return nil
	}, "2m", "5s").Should(Succeed(), "Validating webhook never became reachable")
})

var _ = AfterSuite(func() {
	// Belt-and-suspenders: even if BeforeSuite somehow proceeded against a
	// non-kind context (e.g. a concurrent context switch), refuse to run
	// make undeploy / make uninstall which would delete the CRD and cascade-
	// delete all HermesAgent CRs on the cluster.
	ctx, ctxErr := currentKubectlContext()
	if ctxErr != nil || !isKindContext(ctx) {
		_, _ = fmt.Fprintf(GinkgoWriter,
			"WARNING: AfterSuite teardown SKIPPED — kubectl context %q is not a kind cluster "+
				"(or could not be determined). Refusing to run make undeploy / make uninstall "+
				"to avoid data loss on a non-kind cluster. Manual cleanup required.\n",
			ctx)
		return
	}

	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, _ = utils.Run(cmd)

	By("uninstalling CRDs")
	cmd = exec.Command("make", "uninstall")
	_, _ = utils.Run(cmd)

	By("removing manager namespace")
	cmd = exec.Command("kubectl", "delete", "ns", "hermes-operator-system")
	_, _ = utils.Run(cmd)

	By("removing test namespace + leftover CRBs")
	cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found=true")
	_, _ = utils.Run(cmd)
	_ = exec.Command("kubectl", "delete", "clusterrolebinding",
		"hermes-operator-metrics-binding", "--ignore-not-found=true").Run()

	// Teardown CertManager after the suite if not skipped and if it was not already installed
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})
