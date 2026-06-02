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
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// pkgDir is the absolute path of the test/e2e package directory. Used to
// resolve fixture + repo-root manifests independently of the working
// directory the test binary inherits (which differs between `go test` from
// project root and IDE-launched runs).
var pkgDir = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}()

// vapManifestPath returns the absolute path of a file under
// test/e2e/testdata/vap/. Anchored to pkgDir so the path is correct
// regardless of test cwd.
func vapManifestPath(filename string) string {
	return filepath.Join(pkgDir, "testdata", "vap", filename)
}

// vapPolicyManifestPath returns the absolute path of the VAP manifest in
// config/admission-policy/. Walks up from pkgDir (test/e2e) to the repo
// root, then down into config/.
func vapPolicyManifestPath() string {
	return filepath.Join(pkgDir, "..", "..", "config", "admission-policy",
		"validatingadmissionpolicy.yaml")
}

// vapTestNamespace is the per-suite namespace where the VAP e2e creates
// session pods (compliant + intentionally non-compliant). The namespace
// name is referenced by the VAP testdata fixtures verbatim — changing it
// here requires updating every file under test/e2e/testdata/vap/.
const vapTestNamespace = "hermes-vap-e2e"

// vapTestSA is the test ServiceAccount the e2e impersonates to attempt pod
// creates. The local-part MUST start with `hermes-` to match the VAP's
// identity matchCondition regex `^system:serviceaccount:[^:]+:hermes-[^:]+$`.
// Otherwise the VAP doesn't apply and the spec would test the wrong path.
const vapTestSA = "hermes-vap-test"

// kubectlAsTestSA runs a kubectl apply impersonating the test SA. Returns the
// combined kubectl output (so callers can assert on VAP deny messages) and
// the exit error.
func kubectlAsTestSA(args ...string) (string, error) {
	all := append([]string{"--as",
		fmt.Sprintf("system:serviceaccount:%s:%s", vapTestNamespace, vapTestSA)},
		args...)
	cmd := exec.Command("kubectl", all...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// applyManifestFile applies a YAML file from disk via kubectl. Used by the VAP
// e2e to push prebuilt fixture pods that exercise specific deny paths.
func applyManifestFile(path string) (string, error) {
	cmd := exec.Command("kubectl", "apply", "-f", path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// applyManifestFileAsTestSA combines kubectl apply with --as impersonation.
// The fixture flow: BeforeAll installs the VAP + creates the test namespace +
// SAs + Role granting pods:create. Each It block does
// kubectlAsTestSA("apply", "-f", "<fixture>") and asserts the API server
// either Allow (compliant) or Deny (non-compliant).
func applyManifestFileAsTestSA(path string) (string, error) {
	return kubectlAsTestSA("apply", "-f", path)
}

var _ = Describe("session-pod VAP enforcement (Phase 10.7)", Ordered, func() {
	BeforeAll(func() {
		By("installing the ValidatingAdmissionPolicy + binding")
		out, err := applyManifestFile(vapPolicyManifestPath())
		Expect(err).NotTo(HaveOccurred(), out)

		By("creating the test namespace, SAs, and pods/create Role")
		out, err = applyManifestFile(vapManifestPath("test-rbac.yaml"))
		Expect(err).NotTo(HaveOccurred(), out)

		// VAP `typeChecking` is async — the policy needs a beat before the
		// matchCondition + validations are evaluated on incoming requests.
		// Probe by trying the compliant fixture until it either succeeds or
		// fails with the expected NotFound (already created on a previous
		// pass). Failing with a VAP deny message would mean policy + binding
		// loaded — fixture issue. Failing with anything else (typeChecking
		// errors, webhook not ready) is a transient we'll Eventually past.
		By("waiting for the VAP to become enforceable on pod creates")
		Eventually(func() error {
			// Use a probe pod name distinct from the spec fixtures so the
			// probe doesn't pre-create resources the It-blocks depend on.
			probe := `apiVersion: v1
kind: Pod
metadata:
  name: hermes-ws-vap-probe
  namespace: hermes-vap-e2e
  labels:
    app.kubernetes.io/managed-by: hermes-agent
spec:
  serviceAccountName: hermes-vap-session
  automountServiceAccountToken: false
  restartPolicy: Never
  containers:
    - name: probe
      image: docker.io/library/ubuntu:22.04
      command: ["sleep", "infinity"]
      resources:
        limits: { cpu: "100m", memory: "64Mi" }
      securityContext:
        runAsNonRoot: true
        allowPrivilegeEscalation: false
        capabilities: { drop: ["ALL"] }
`
			c := exec.Command("kubectl", "--as",
				fmt.Sprintf("system:serviceaccount:%s:%s", vapTestNamespace, vapTestSA),
				"apply", "-f", "-")
			c.Stdin = strings.NewReader(probe)
			out, err := c.CombinedOutput()
			if err == nil {
				// Allowed — VAP is enforceable and the compliant shape works.
				_ = exec.Command("kubectl", "-n", vapTestNamespace, "delete", "pod",
					"hermes-ws-vap-probe", "--ignore-not-found=true").Run()
				return nil
			}
			s := string(out)
			// Recognise "VAP loaded and denied" as success — the VAP is up.
			if strings.Contains(s, "ValidatingAdmissionPolicy") ||
				strings.Contains(s, "hermes-session-pod-security") {
				return nil
			}
			return fmt.Errorf("VAP not ready: %s", strings.TrimSpace(s))
		}, "60s", "5s").Should(Succeed())
	})

	AfterAll(func() {
		By("tearing down the VAP e2e namespace + policy")
		_ = exec.Command("kubectl", "delete", "namespace", vapTestNamespace,
			"--ignore-not-found=true").Run()
		_ = exec.Command("kubectl", "delete", "validatingadmissionpolicybinding",
			"hermes-session-pod-security", "--ignore-not-found=true").Run()
		_ = exec.Command("kubectl", "delete", "validatingadmissionpolicy",
			"hermes-session-pod-security", "--ignore-not-found=true").Run()
	})

	It("denies a privileged hermes session pod (hostPID: true)", func() {
		out, err := applyManifestFileAsTestSA(vapManifestPath("privileged-session-pod.yaml"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("hostPID is not allowed"))
	})

	It("denies a pod without the managed-by label (label-bypass regression)", func() {
		// Regression test for the original agent-sandbox bypass: a compromised
		// agent omits the managed-by label hoping the VAP will skip. With
		// label-as-validation, the VAP runs and DENIES instead.
		out, err := applyManifestFileAsTestSA(vapManifestPath("unlabeled-session-pod.yaml"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("managed-by=hermes-agent label"))
	})

	It("denies a pod using a non-session ServiceAccount", func() {
		out, err := applyManifestFileAsTestSA(vapManifestPath("wrong-sa-session-pod.yaml"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("session SA"))
	})

	It("denies a pod with no resource limits", func() {
		out, err := applyManifestFileAsTestSA(vapManifestPath("no-limits-session-pod.yaml"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("cpu + memory limits"))
	})

	It("denies a pod mounting a PVC outside the hermes-ws-* allowlist", func() {
		out, err := applyManifestFileAsTestSA(vapManifestPath("wrong-pvc-session-pod.yaml"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("hermes-ws-"))
	})

	It("allows a fully-compliant hermes session pod", func() {
		out, err := applyManifestFileAsTestSA(vapManifestPath("compliant-session-pod.yaml"))
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("created"))
	})
})
