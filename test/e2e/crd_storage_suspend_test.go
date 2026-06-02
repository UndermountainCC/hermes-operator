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

var _ = Describe("CRD storage.existingClaimName", Ordered, func() {
	const agentName = "adopt-pvc"
	const claimName = "legacy-claim"

	BeforeAll(func() { ensureTestNamespace() })
	AfterAll(func() {
		deleteAgentCR(agentName, testNamespace)
		_ = exec.Command("kubectl", "-n", testNamespace, "delete", "pvc", claimName, "--ignore-not-found=true").Run()
	})

	It("mounts a pre-existing PVC and does not generate hermes-<name>-data", func() {
		By("pre-creating the legacy-named PVC")
		pvc := fmt.Sprintf(`
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: %s, namespace: %s }
spec:
  accessModes: [ReadWriteOnce]
  resources: { requests: { storage: 1Gi } }
`, claimName, testNamespace)
		out, err := applyManifest(pvc)
		Expect(err).NotTo(HaveOccurred(), "pre-create PVC: %s", out)

		By("applying a HermesAgent that adopts it via existingClaimName")
		cr := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: %s
  storage:
    existingClaimName: %s
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env: [{ name: DEEPSEEK_API_KEY, value: "sk-placeholder" }]
`, agentName, testNamespace, hermesAgentImage, claimName)
		out, err = applyManifest(cr)
		Expect(err).NotTo(HaveOccurred(), "apply CR: %s", out)

		By("verifying the Deployment mounts the legacy claim, not a generated one")
		Eventually(func() string {
			o, _ := utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
				"deployment", fmt.Sprintf("hermes-%s", agentName),
				"-o", `jsonpath={.spec.template.spec.volumes[?(@.name=="data")].persistentVolumeClaim.claimName}`))
			return strings.TrimSpace(o)
		}, "60s", "2s").Should(Equal(claimName))

		By("verifying the operator did NOT create hermes-<name>-data")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
			"pvc", fmt.Sprintf("hermes-%s-data", agentName)))
		Expect(err).To(HaveOccurred(), "operator must not generate a PVC when adopting")
	})

	It("rejects a CR that sets both existingClaimName and a populated persistentVolumeClaim", func() {
		manifest := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: both-storage, namespace: %s }
spec:
  image: %s
  storage:
    existingClaimName: foo
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
`, testNamespace, hermesAgentImage)
		out, err := applyManifest(manifest)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("set exactly one of storage"))
	})
})

var _ = Describe("CRD spec.suspend", Ordered, func() {
	const agentName = "suspendable"

	BeforeAll(func() { ensureTestNamespace() })
	AfterAll(func() { deleteAgentCR(agentName, testNamespace) })

	It("scales to 0 on suspend=true and back to 1 on suspend=false, keeping the PVC", func() {
		cr := fmt.Sprintf(`
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: %s, namespace: %s }
spec:
  image: %s
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env: [{ name: DEEPSEEK_API_KEY, value: "sk-placeholder" }]
`, agentName, testNamespace, hermesAgentImage)
		_, err := applyManifest(cr)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the Deployment to exist at replicas=1")
		Eventually(func() string {
			o, _ := utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
				"deployment", fmt.Sprintf("hermes-%s", agentName), "-o", "jsonpath={.spec.replicas}"))
			return strings.TrimSpace(o)
		}, "60s", "2s").Should(Equal("1"))

		By("suspending")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace, "patch",
			"hermesagent", agentName, "--type=merge", "-p", `{"spec":{"suspend":true}}`))
		Expect(err).NotTo(HaveOccurred())

		By("Deployment scales to 0 and phase becomes Suspended")
		Eventually(func() string {
			o, _ := utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
				"deployment", fmt.Sprintf("hermes-%s", agentName), "-o", "jsonpath={.spec.replicas}"))
			return strings.TrimSpace(o)
		}, "60s", "2s").Should(Equal("0"))
		Eventually(func() string {
			return getAgentField(agentName, testNamespace, "{.status.phase}")
		}, "60s", "2s").Should(Equal("Suspended"))

		By("PVC still present while suspended")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
			"pvc", fmt.Sprintf("hermes-%s-data", agentName)))
		Expect(err).NotTo(HaveOccurred())

		By("unsuspending scales back to 1")
		_, err = utils.Run(exec.Command("kubectl", "-n", testNamespace, "patch",
			"hermesagent", agentName, "--type=merge", "-p", `{"spec":{"suspend":false}}`))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string {
			o, _ := utils.Run(exec.Command("kubectl", "-n", testNamespace, "get",
				"deployment", fmt.Sprintf("hermes-%s", agentName), "-o", "jsonpath={.spec.replicas}"))
			return strings.TrimSpace(o)
		}, "60s", "2s").Should(Equal("1"))
	})
})
