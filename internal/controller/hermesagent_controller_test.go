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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap/zaptest/observer"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

const (
	// timeout is the Eventually-wait used by envtest specs. Bumped from 10s to
	// 15s when Phase 10.7 (exec-backend RBAC) added another per-agent reconcile
	// step. The added step is short-circuited for default-spec agents (see
	// exec_rbac.go hasExecBackendReadyCondition), but the additional work
	// during specs that DO enable kubernetes still pushed a few existing
	// Eventually waits past 10s under contention from 32 concurrent specs.
	// 15s adds headroom without slowing the happy path (Eventually exits as
	// soon as the assertion passes).
	timeout   = 15 * time.Second
	interval  = 250 * time.Millisecond
	namespace = "default"
)

var _ = Describe("HermesAgent reconciliation", func() {
	It("creates PVC, SA, and Deployment for a basic agent", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image:           "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				LLMDefaultProvider: "deepseek",
				LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
					{
						Name: "deepseek",
						Env: []corev1.EnvVar{
							{Name: "DEEPSEEK_API_KEY", Value: "k"},
						},
					},
				},
				Gateways: []hermesv1alpha1.HermesAgentGateway{
					{Type: "discord", Env: []corev1.EnvVar{{Name: "DISCORD_BOT_TOKEN", Value: "t"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// PVC
		Eventually(func() error {
			var pvc corev1.PersistentVolumeClaim
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-smoke-data", Namespace: namespace}, &pvc)
		}, timeout, interval).Should(Succeed())

		// ServiceAccount
		Eventually(func() error {
			var sa corev1.ServiceAccount
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-smoke", Namespace: namespace}, &sa)
		}, timeout, interval).Should(Succeed())

		// Deployment
		var dep appsv1.Deployment
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-smoke", Namespace: namespace}, &dep)
		}, timeout, interval).Should(Succeed())

		// Spot-check the Deployment.
		Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.Image).To(Equal(agent.Spec.Image))
		Expect(c.Args).To(Equal([]string{"gateway", "run"}))

		// Env composition: HERMES_INFERENCE_PROVIDER must be present.
		var found bool
		for _, e := range c.Env {
			if e.Name == "HERMES_INFERENCE_PROVIDER" && e.Value == "deepseek" {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "HERMES_INFERENCE_PROVIDER not stamped on container env")

		// Status phase should eventually be Provisioning (no real kubelet in envtest;
		// pod never goes Ready, so we settle at Provisioning).
		Eventually(func() hermesv1alpha1.HermesAgentPhase {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "smoke", Namespace: namespace}, &got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, timeout, interval).Should(Equal(hermesv1alpha1.PhaseProvisioning))
	})
})

var _ = Describe("HermesAgent storage.existingClaimName (adopt a pre-existing PVC)", func() {
	// Regression: the storage XValidation CEL rule dereferenced
	// self.persistentVolumeClaim.accessModes without first checking the key
	// exists. The controller-runtime client serializes the zero-value
	// PersistentVolumeClaimSpec as a present-but-empty object (Go's json
	// omitempty is a no-op on structs), so once the operator wrote the CR back
	// to add its finalizer, CEL evaluated `size(self.persistentVolumeClaim.
	// accessModes)` against an object with no accessModes key and errored with
	// "no such key: accessModes". The Update was rejected, the finalizer never
	// landed, and reconcile looped forever without ever creating the Deployment.
	It("adds the finalizer and creates a Deployment mounting the adopted claim, generating no PVC", func() {
		scn := "standard"
		legacy := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-claim", Namespace: namespace},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &scn,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		Expect(k8sClient.Create(ctx, legacy)).To(Succeed())

		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					ExistingClaimName: "legacy-claim",
				},
				LLMDefaultProvider: "deepseek",
				LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
					{Name: "deepseek", Env: []corev1.EnvVar{{Name: "DEEPSEEK_API_KEY", Value: "k"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// The exact write the CEL bug rejected: the operator's finalizer Update.
		Eventually(func() []string {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "adopt", Namespace: namespace}, &got); err != nil {
				return nil
			}
			return got.Finalizers
		}, timeout, interval).Should(ContainElement(finalizerName))

		// A Deployment that mounts the adopted claim verbatim, not a generated one.
		var dep appsv1.Deployment
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-adopt", Namespace: namespace}, &dep)
		}, timeout, interval).Should(Succeed())

		var dataVol *corev1.Volume
		for i := range dep.Spec.Template.Spec.Volumes {
			if dep.Spec.Template.Spec.Volumes[i].Name == "data" {
				dataVol = &dep.Spec.Template.Spec.Volumes[i]
				break
			}
		}
		Expect(dataVol).NotTo(BeNil(), "Deployment has no 'data' volume")
		Expect(dataVol.PersistentVolumeClaim).NotTo(BeNil(), "'data' volume is not PVC-backed")
		Expect(dataVol.PersistentVolumeClaim.ClaimName).To(Equal("legacy-claim"))

		// The operator must not generate its own PVC when adopting.
		Consistently(func() error {
			var pvc corev1.PersistentVolumeClaim
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-adopt-data", Namespace: namespace}, &pvc)
		}, "2s", interval).ShouldNot(Succeed())
	})
})

var _ = Describe("HermesAgent Deployment invariants", func() {
	It("hardcodes replicas=1 and strategy=Recreate on the agent Deployment", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "replicas-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-replicas-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
				return false
			}
			if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
				return false
			}
			return true
		}, timeout, interval).Should(BeTrue())
	})

	It("sets terminationGracePeriodSeconds=210 to honor gateway drain timeout", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "tgs-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() int64 {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-tgs-test", Namespace: namespace}, &dep); err != nil {
				return 0
			}
			if dep.Spec.Template.Spec.TerminationGracePeriodSeconds == nil {
				return 0
			}
			return *dep.Spec.Template.Spec.TerminationGracePeriodSeconds
		}, timeout, interval).Should(Equal(int64(210)))
	})
})

var _ = Describe("HermesAgent self-introspection RBAC (Phase 10.6)", func() {
	It("creates a per-agent self Role + RoleBinding pinned to the agent's own resourceNames", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "self-rbac", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Role exists with the expected name + rules
		var role rbacv1.Role
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-self-rbac-self", Namespace: namespace}, &role)
		}, timeout, interval).Should(Succeed())

		Expect(role.Rules).To(HaveLen(2))

		// Deployment rule: get + patch, resourceName-pinned
		depRule := role.Rules[0]
		Expect(depRule.APIGroups).To(Equal([]string{"apps"}))
		Expect(depRule.Resources).To(Equal([]string{"deployments"}))
		Expect(depRule.ResourceNames).To(Equal([]string{"hermes-self-rbac"}))
		Expect(depRule.Verbs).To(ConsistOf("get", "patch"))

		// CR rule: get only, resourceName-pinned
		crRule := role.Rules[1]
		Expect(crRule.APIGroups).To(Equal([]string{"hermes.k8s.undermountain.cc"}))
		Expect(crRule.Resources).To(Equal([]string{"hermesagents"}))
		Expect(crRule.ResourceNames).To(Equal([]string{"self-rbac"}))
		Expect(crRule.Verbs).To(ConsistOf("get"))

		// RoleBinding exists and ties the SA to the Role
		var rb rbacv1.RoleBinding
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-self-rbac-self", Namespace: namespace}, &rb)
		}, timeout, interval).Should(Succeed())

		Expect(rb.RoleRef.Kind).To(Equal("Role"))
		Expect(rb.RoleRef.Name).To(Equal("hermes-self-rbac-self"))
		Expect(rb.Subjects).To(HaveLen(1))
		Expect(rb.Subjects[0].Kind).To(Equal("ServiceAccount"))
		Expect(rb.Subjects[0].Name).To(Equal("hermes-self-rbac"))
		Expect(rb.Subjects[0].Namespace).To(Equal(namespace))

		// CR ownerRef is set on the Role + RoleBinding (same-namespace, so
		// ownerRef is legal — applyObject path).
		Expect(role.OwnerReferences).To(HaveLen(1))
		Expect(role.OwnerReferences[0].Name).To(Equal("self-rbac"))
		Expect(rb.OwnerReferences).To(HaveLen(1))
		Expect(rb.OwnerReferences[0].Name).To(Equal("self-rbac"))

		// Cleanup: CR delete cascades via ownerRef (no finalizer dance for
		// same-namespace children).
		Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
	})

	It("does NOT create a self Role/RoleBinding when spec.serviceAccountName is user-provided (BYO opt-out)", func() {
		// BYO SAs may be shared across multiple HermesAgent CRs; layering
		// per-agent self-Roles onto a shared identity would create
		// cross-agent restart leaks. The operator opts out entirely when
		// the user manages the SA.

		// Pre-create the external SA so reconcileServiceAccount's existence
		// check passes.
		externalSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-external-sa", Namespace: namespace},
		}
		Expect(k8sClient.Create(ctx, externalSA)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, externalSA) })

		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-sa", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image:              "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				ServiceAccountName: "byo-external-sa",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

		// Reconcile happens; wait for the Deployment to land (proxy for
		// "reconcile finished a cycle") before asserting RBAC absence.
		Eventually(func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-sa", Namespace: namespace}, &dep)
		}, timeout, interval).Should(Succeed())

		// Self Role + RoleBinding must NOT exist — operator opted out.
		var role rbacv1.Role
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-sa-self", Namespace: namespace}, &role)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "self Role should NOT exist for BYO-SA agents")

		var rb rbacv1.RoleBinding
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-sa-self", Namespace: namespace}, &rb)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "self RoleBinding should NOT exist for BYO-SA agents")
	})

	It("deletes the self Role + RoleBinding when an agent toggles INTO BYO-SA mode", func() {
		// Pre-create the external SA that the agent will toggle to.
		externalSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-toggle-sa", Namespace: namespace},
		}
		Expect(k8sClient.Create(ctx, externalSA)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, externalSA) })

		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-toggle", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

		// Wait for the operator-managed self Role to exist (default-SA path).
		Eventually(func() error {
			var role rbacv1.Role
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-toggle-self", Namespace: namespace}, &role)
		}, timeout, interval).Should(Succeed())

		// Toggle to BYO by patching spec.serviceAccountName.
		patch := client.MergeFrom(agent.DeepCopy())
		agent.Spec.ServiceAccountName = "byo-toggle-sa"
		Expect(k8sClient.Patch(ctx, agent, patch)).To(Succeed())

		// Self Role + RoleBinding should be garbage-collected.
		Eventually(func() bool {
			var role rbacv1.Role
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-toggle-self", Namespace: namespace}, &role)
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "self Role should be deleted after BYO-SA toggle")

		Eventually(func() bool {
			var rb rbacv1.RoleBinding
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-byo-toggle-self", Namespace: namespace}, &rb)
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "self RoleBinding should be deleted after BYO-SA toggle")
	})
})

var _ = Describe("HermesAgent RBAC reconciliation", func() {
	It("creates RoleBindings and ClusterRoleBindings, rejects out-of-allowlist roles, cleans up on delete", func() {
		// First: a CR with cluster-admin (allowed in test setup) + a namespace RoleBinding.
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "rbac-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				RBAC: hermesv1alpha1.HermesAgentRBAC{
					RoleBindings: []hermesv1alpha1.HermesAgentRoleBinding{
						{
							Namespace: "default",
							RoleRef: rbacv1.RoleRef{
								APIGroup: "rbac.authorization.k8s.io",
								Kind:     "ClusterRole",
								Name:     "admin",
							},
						},
					},
					ClusterRoleBindings: []hermesv1alpha1.HermesAgentClusterRoleBinding{
						{RoleRef: rbacv1.RoleRef{
							APIGroup: "rbac.authorization.k8s.io",
							Kind:     "ClusterRole",
							Name:     "cluster-admin",
						}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// RoleBinding exists
		Eventually(func() error {
			var rb rbacv1.RoleBinding
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-rbac-test-0", Namespace: "default"}, &rb)
		}, timeout, interval).Should(Succeed())

		// ClusterRoleBinding exists
		Eventually(func() error {
			var crb rbacv1.ClusterRoleBinding
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-rbac-test-cluster-0"}, &crb)
		}, timeout, interval).Should(Succeed())

		// Delete the CR and verify cleanup via finalizer.
		Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
		Eventually(func() bool {
			var crb rbacv1.ClusterRoleBinding
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-rbac-test-cluster-0"}, &crb)
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})

	It("rejects ClusterRoleBindings whose role is not in the allowlist", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "rbac-reject", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				RBAC: hermesv1alpha1.HermesAgentRBAC{
					ClusterRoleBindings: []hermesv1alpha1.HermesAgentClusterRoleBinding{
						{RoleRef: rbacv1.RoleRef{
							APIGroup: "rbac.authorization.k8s.io",
							Kind:     "ClusterRole",
							Name:     "system:masters", // NOT in test allowlist
						}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Expect RBACSynced=False condition with allowlist-violation reason.
		Eventually(func() string {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rbac-reject", Namespace: namespace}, &got); err != nil {
				return ""
			}
			for _, c := range got.Status.Conditions {
				if c.Type == hermesv1alpha1.ConditionRBACSynced {
					return c.Reason
				}
			}
			return ""
		}, timeout, interval).Should(Equal("RBACPolicyViolation"))
	})
})

var _ = Describe("HermesAgent reconcile resilience", func() {
	// Reproduces a concurrency bug surfaced by QA against a real (kind)
	// cluster: applyObject does Get → mutate → Update. In production K8s,
	// the built-in Deployment controller bumps Deployment.status fields
	// between our Get and Update, invalidating our resourceVersion. Our
	// Update then fires with stale rv → "Operation cannot be fulfilled
	// ... the object has been modified". controller-runtime requeues and
	// eventually converges, but emits ERROR-level "Reconciler error" logs
	// signalling a brittle reconcile pattern.
	//
	// envtest does NOT run kube-controller-manager, so single-threaded
	// CR updates don't reproduce the race naturally. We inject the race
	// directly: a test goroutine bumps Deployment.status concurrently with
	// the operator's reconcile loop, simulating what kube-controller-manager
	// does in real clusters.
	//
	// The fix is server-side apply (Patch with client.Apply + FieldOwner).
	// SSA merges at the field level and doesn't care about rv conflicts on
	// fields the operator doesn't manage. Pre-fix this test FAILS (counts
	// > 0 errors). Post-fix it PASSES (counts == 0).
	It("does not log reconciler errors when Deployment status is concurrently bumped", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "resilience", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image:           "ghcr.io/example/hermes@sha256:1111111111111111111111111111111111111111111111111111111111111111",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		crKey := types.NamespacedName{Name: "resilience", Namespace: namespace}
		depKey := types.NamespacedName{Name: "hermes-resilience", Namespace: namespace}

		// Wait for first reconcile to produce the Deployment.
		Eventually(func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, depKey, &dep)
		}, timeout, interval).Should(Succeed())

		// Reset observer AFTER the first reconcile cycle. The cold-start
		// reconcile may produce one transient error as the controller's
		// cache primes — we care about steady-state, not boot.
		observedLogs.TakeAll()

		// Goroutine: bump Deployment.status.observedGeneration on a 10ms
		// cadence to simulate kube-controller-manager touching the object.
		// This is exactly the scenario we hit in the kind smoke: status
		// fields tick under the operator's feet, invalidating its rv.
		racerDone := make(chan struct{})
		go func() {
			defer close(racerDone)
			for i := 0; i < 60; i++ {
				var dep appsv1.Deployment
				if err := k8sClient.Get(ctx, depKey, &dep); err == nil {
					dep.Status.ObservedGeneration++
					_ = k8sClient.Status().Update(ctx, &dep)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		// In parallel, trigger 5 CR updates to fire reconciles.
		for i := 0; i < 5; i++ {
			Eventually(func() error {
				var live hermesv1alpha1.HermesAgent
				if err := k8sClient.Get(ctx, crKey, &live); err != nil {
					return err
				}
				if live.Annotations == nil {
					live.Annotations = map[string]string{}
				}
				live.Annotations["bump"] = fmt.Sprintf("%d-%d", i, time.Now().UnixNano())
				return k8sClient.Update(ctx, &live)
			}, timeout, interval).Should(Succeed())
			time.Sleep(50 * time.Millisecond)
		}

		<-racerDone
		time.Sleep(2 * time.Second) // drain reconcile queue

		// "Reconciler error" is controller-runtime's log key when Reconcile
		// returns non-nil. Our applyObject Update conflicts propagate here.
		// Filter to errors involving THIS test's agent ("resilience") — other
		// tests (e.g., rbac-reject which intentionally produces RBAC errors)
		// leave CRs behind that the controller keeps reconciling; those errors
		// are not this test's concern.
		reconcileErrors := observedLogs.Filter(func(e observer.LoggedEntry) bool {
			if e.Message != "Reconciler error" {
				return false
			}
			for _, f := range e.Context {
				if f.Key == "HermesAgent" {
					if nsn, ok := f.Interface.(types.NamespacedName); ok {
						return nsn.Name == "resilience"
					}
				}
			}
			return false
		}).All()
		Expect(reconcileErrors).To(BeEmpty(),
			"expected zero reconciler errors for 'resilience' agent during racing Deployment.status updates, got %d: %+v",
			len(reconcileErrors), reconcileErrors)
	})
})

var _ = Describe("HermesAgent secrets gate", func() {
	It("blocks Deployment creation when a Secret ref is missing", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
					{Name: "deepseek", Env: []corev1.EnvVar{
						{Name: "DEEPSEEK_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "not-yet-created"}, Key: "k",
						}}},
					}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() hermesv1alpha1.HermesAgentPhase {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "bootstrap-test", Namespace: namespace}, &got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, timeout, interval).Should(Equal(hermesv1alpha1.PhaseBootstrap))

		var dep appsv1.Deployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-bootstrap-test", Namespace: namespace}, &dep)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// Create the Secret, then expect reconciliation to progress.
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "not-yet-created", Namespace: namespace},
			Data:       map[string][]byte{"k": []byte("v")},
		})).To(Succeed())

		// The secret-gate failure path returns RequeueAfter=15s, and the
		// controller does not watch Secrets — so the next reconcile that
		// re-validates and creates the Deployment fires on that timer (or
		// sooner, if any other CR event lands). Allow up to 30s here.
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-bootstrap-test", Namespace: namespace}, &dep)
		}, 30*time.Second, interval).Should(Succeed())
	})
})

var _ = Describe("HermesAgent readiness contract (Phase 7a)", func() {
	It("emits exec-probes targeting hermes gateway status on the agent container", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "exec-probe-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-exec-probe-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			if len(dep.Spec.Template.Spec.Containers) == 0 {
				return false
			}
			c := dep.Spec.Template.Spec.Containers[0]
			if c.ReadinessProbe == nil || c.ReadinessProbe.Exec == nil {
				return false
			}
			cmd := c.ReadinessProbe.Exec.Command
			if len(cmd) != 3 || cmd[0] != "/bin/bash" || cmd[1] != "-c" {
				return false
			}
			if !strings.Contains(cmd[2], "hermes gateway status") {
				return false
			}
			if !strings.Contains(cmd[2], "Gateway is running") {
				return false
			}
			// Liveness too — uses the same exec handler.
			if c.LivenessProbe == nil || c.LivenessProbe.Exec == nil {
				return false
			}
			return true
		}, timeout, interval).Should(BeTrue())
	})

	It("leaves status.gateways[] empty even when spec.gateways[] is configured (Phase 7a)", func() {
		// Phase 3 contract was: populate status.gateways[] from /health/detailed
		// probe. Phase 7a drops the probe entirely (gateway run binds no HTTP);
		// the field stays nil until Phase 7b's dashboard sidecar lands.
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-gateway-status-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:2222222222222222222222222222222222222222222222222222222222222222",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Gateways: []hermesv1alpha1.HermesAgentGateway{
					{Type: "discord", Env: []corev1.EnvVar{{Name: "DISCORD_TOKEN", Value: "t"}}},
					{Type: "telegram", Env: []corev1.EnvVar{{Name: "TELEGRAM_TOKEN", Value: "t"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		depKey := types.NamespacedName{Name: "hermes-no-gateway-status-test", Namespace: namespace}

		// Wait for Deployment to be created.
		Eventually(func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, depKey, &dep)
		}, timeout, interval).Should(Succeed())

		// Force the Deployment to appear ready (envtest has no kubelet).
		Eventually(func() error {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, depKey, &dep); err != nil {
				return err
			}
			dep.Status.ReadyReplicas = 1
			dep.Status.Replicas = 1
			dep.Status.AvailableReplicas = 1
			return k8sClient.Status().Update(ctx, &dep)
		}, timeout, interval).Should(Succeed())

		crKey := types.NamespacedName{Name: "no-gateway-status-test", Namespace: namespace}

		// Assert: phase reaches Ready on pod readiness alone (no longer
		// gated by per-gateway probe).
		Eventually(func() hermesv1alpha1.HermesAgentPhase {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, crKey, &got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, timeout, interval).Should(Equal(hermesv1alpha1.PhaseReady))

		// Assert: status.gateways stays empty (no probe to populate it).
		var got hermesv1alpha1.HermesAgent
		Expect(k8sClient.Get(ctx, crKey, &got)).To(Succeed())
		Expect(got.Status.Gateways).To(BeEmpty())

		// Assert: no GatewaysReady condition is emitted (dashboard-sidecar-only).
		for _, c := range got.Status.Conditions {
			Expect(c.Type).NotTo(Equal(hermesv1alpha1.ConditionGatewaysReady),
				"GatewaysReady condition should not be emitted in Phase 7a")
		}
	})

	It("rejects CRs missing required fields via admission webhook", func() {
		// Bypass the reconciler — this CR should be refused at admit time and
		// never reach the cache.
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "webhook-reject", Namespace: namespace},
			Spec:       hermesv1alpha1.HermesAgentSpec{
				// Image deliberately empty so the validating webhook fires.
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.image"))
	})

	It("rejects llmDefaultProvider that doesn't match a provider name via admission webhook", func() {
		storageClass := "standard"
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "webhook-reject-defaultprovider", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "img",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				LLMDefaultProvider: "deepseek",
				LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
					{Name: "anthropic"},
				},
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.llmDefaultProvider"))
	})
})

var _ = Describe("HermesAgent dashboard probe → status.gateways[] (Phase 7b)", func() {
	It("populates status.gateways[] from the dashboard probe with state-aware readiness", func() {
		originalFn := reconciler.ProbeHealthFn
		reconciler.ProbeHealthFn = func(_ context.Context, _ string) (*DashboardStatus, error) {
			return &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "running",
				GatewayPlatforms: map[string]PlatformState{
					"discord":  {State: "connected"},
					"slack":    {State: "connecting"},
					"telegram": {State: "fatal", ErrorMessage: "invalid bot token"},
				},
			}, nil
		}
		DeferCleanup(func() { reconciler.ProbeHealthFn = originalFn })

		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "probe-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Gateways: []hermesv1alpha1.HermesAgentGateway{
					{Type: "discord"},
					{Type: "slack"},
					{Type: "telegram"},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "probe-test", Namespace: namespace}, &got); err != nil {
				return false
			}
			if len(got.Status.Gateways) != 3 {
				return false
			}
			m := map[string]hermesv1alpha1.HermesAgentGatewayStatus{}
			for _, g := range got.Status.Gateways {
				m[g.Type] = g
			}
			// discord: connected → Ready=true, State="connected", no Message
			if !m["discord"].Ready || m["discord"].State != "connected" || m["discord"].Message != "" {
				return false
			}
			// slack: connecting → Ready=false, State="connecting"
			if m["slack"].Ready || m["slack"].State != "connecting" {
				return false
			}
			// telegram: fatal → Ready=false, State="fatal", Message populated
			if m["telegram"].Ready || m["telegram"].State != "fatal" || m["telegram"].Message == "" {
				return false
			}
			return true
		}, timeout, interval).Should(BeTrue())
	})

	It("maps top-level gateway_state=degraded to Phase=Degraded", func() {
		originalFn := reconciler.ProbeHealthFn
		reconciler.ProbeHealthFn = func(_ context.Context, _ string) (*DashboardStatus, error) {
			return &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "degraded",
				GatewayPlatforms: map[string]PlatformState{
					"discord":  {State: "connected"},
					"telegram": {State: "fatal", ErrorMessage: "invalid bot token"},
				},
			}, nil
		}
		DeferCleanup(func() { reconciler.ProbeHealthFn = originalFn })

		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "degraded-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Gateways: []hermesv1alpha1.HermesAgentGateway{
					{Type: "discord"},
					{Type: "telegram"},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() hermesv1alpha1.HermesAgentPhase {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "degraded-test", Namespace: namespace}, &got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, timeout, interval).Should(Equal(hermesv1alpha1.PhaseDegraded))
	})
})

var _ = Describe("HermesAgent dashboard sidecar (Phase 7b)", func() {
	It("injects the dashboard sidecar when spec.dashboard.enabled", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-sidecar-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-dashboard-sidecar-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			containers := dep.Spec.Template.Spec.Containers
			if len(containers) != 2 {
				return false
			}
			var dash *corev1.Container
			for i := range containers {
				if containers[i].Name == "dashboard" {
					dash = &containers[i]
				}
			}
			if dash == nil {
				return false
			}
			// Must NOT carry --tui flag.
			for _, arg := range dash.Command {
				if arg == "--tui" {
					return false
				}
			}
			// Upstream dashboard reads no external token env var; the operator
			// must NOT plumb HERMES_DASHBOARD_TOKEN onto the container.
			for _, e := range dash.Env {
				if e.Name == "HERMES_DASHBOARD_TOKEN" {
					return false
				}
			}
			// Required args present.
			hasInsecure, hasHost, hasNoOpen := false, false, false
			for _, arg := range dash.Command {
				switch arg {
				case "--insecure":
					hasInsecure = true
				case "--host":
					hasHost = true
				case "--no-open":
					hasNoOpen = true
				}
			}
			return hasInsecure && hasHost && hasNoOpen
		}, timeout, interval).Should(BeTrue())
	})

	It("sets the dashboard container SecurityContext to UID 10000 (hermes user) — not root", func() {
		// Bug A regression (smoke 2026-05-15): the dashboard Command override
		// bypasses the image entrypoint, so without an explicit SecurityContext
		// the dashboard runs as root and writes /opt/data/* root-owned —
		// gateway (UID 10000) then can't read its own state.
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-uid-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		var dep appsv1.Deployment
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-dashboard-uid-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			return len(dep.Spec.Template.Spec.Containers) == 2
		}, timeout, interval).Should(BeTrue())

		var dash, gateway *corev1.Container
		for i := range dep.Spec.Template.Spec.Containers {
			c := &dep.Spec.Template.Spec.Containers[i]
			if c.Name == "dashboard" {
				dash = c
			}
			if c.Name == "hermes" {
				gateway = c
			}
		}
		Expect(dash).NotTo(BeNil())
		Expect(dash.SecurityContext).NotTo(BeNil())
		Expect(dash.SecurityContext.RunAsUser).NotTo(BeNil())
		Expect(*dash.SecurityContext.RunAsUser).To(Equal(int64(10000)))
		Expect(dash.SecurityContext.RunAsGroup).NotTo(BeNil())
		Expect(*dash.SecurityContext.RunAsGroup).To(Equal(int64(10000)))

		// And do NOT set a SecurityContext on the gateway container — it
		// inherits UID 10000 via the upstream image's entrypoint, and forcing
		// it here would shadow any future entrypoint changes upstream makes.
		Expect(gateway).NotTo(BeNil())
		Expect(gateway.SecurityContext).To(BeNil())
	})

	It("does NOT inject the dashboard sidecar when spec.dashboard.enabled is false", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-dashboard-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-dashboard-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			return len(dep.Spec.Template.Spec.Containers) == 1
		}, timeout, interval).Should(BeTrue())
	})

	It("creates a dashboard Service when spec.dashboard.enabled", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-svc-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-dashboard-svc-test-dashboard", Namespace: namespace}, &svc); err != nil {
				return false
			}
			if len(svc.Spec.Ports) != 1 {
				return false
			}
			return svc.Spec.Ports[0].Port == 9119
		}, timeout, interval).Should(BeTrue())
	})

	It("publishes not-ready dashboard endpoints so observability survives gateway outages", func() {
		// Bug B regression (smoke 2026-05-15): when the gateway crashloops,
		// the pod's readiness flips to False, kube-proxy excludes the pod
		// from Service endpoints, and the operator's /api/status probe gets
		// connection refused — wiping the user's visibility into WHY the
		// agent is sick. The dashboard Service must reach the pod even when
		// readiness=False.
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-publish-notready", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-dashboard-publish-notready-dashboard", Namespace: namespace}, &svc); err != nil {
				return false
			}
			return svc.Spec.PublishNotReadyAddresses
		}, timeout, interval).Should(BeTrue())
	})

	It("creates an Ingress when dashboard.ingress.enabled", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "ingress-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{
					Enabled: true,
					Ingress: hermesv1alpha1.HermesAgentDashboardIngress{
						Enabled:          true,
						Host:             "hermes.example.com",
						IngressClassName: "nginx",
						Annotations: map[string]string{
							"nginx.ingress.kubernetes.io/auth-url": "https://auth.example.com/verify",
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var ing networkingv1.Ingress
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-ingress-test-dashboard", Namespace: namespace}, &ing); err != nil {
				return false
			}
			if ing.Annotations["nginx.ingress.kubernetes.io/auth-url"] != "https://auth.example.com/verify" {
				return false
			}
			if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "hermes.example.com" {
				return false
			}
			if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
				return false
			}
			return true
		}, timeout, interval).Should(BeTrue())
	})

	It("does NOT create an Ingress when dashboard.ingress.enabled is false", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-ingress-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Wait for the dashboard Service so we know reconcile ran past Ingress.
		Eventually(func() error {
			var svc corev1.Service
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-ingress-test-dashboard", Namespace: namespace}, &svc)
		}, timeout, interval).Should(Succeed())

		var ing networkingv1.Ingress
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-ingress-test-dashboard", Namespace: namespace}, &ing)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("sets shareProcessNamespace=true on the pod when dashboard.enabled", func() {
		// Upstream's hermes dashboard does PID-based gateway-liveness detection
		// (https://hermes-agent.nousresearch.com/docs/user-guide/docker); without
		// a shared PID namespace it can't see the gateway PID. K8s defaults to
		// isolated PID namespaces per container, so we have to opt in.
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "share-pid-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-share-pid-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			sp := dep.Spec.Template.Spec.ShareProcessNamespace
			return sp != nil && *sp
		}, timeout, interval).Should(BeTrue())
	})

	It("leaves shareProcessNamespace nil when dashboard is disabled", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-share-pid-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Wait for the Deployment, then assert ShareProcessNamespace stayed nil.
		Eventually(func() bool {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-share-pid-test", Namespace: namespace}, &dep); err != nil {
				return false
			}
			return dep.Spec.Template.Spec.ShareProcessNamespace == nil
		}, timeout, interval).Should(BeTrue())
	})

	It("does NOT create any dashboard token Secret (upstream uses ephemeral SPA token)", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-token-secret", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Wait for the dashboard Service so we know reconcile has run past
		// the (now-removed) Secret step.
		Eventually(func() error {
			var svc corev1.Service
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-token-secret-dashboard", Namespace: namespace}, &svc)
		}, timeout, interval).Should(Succeed())

		// No dashboard-token Secret should ever be created — the field doesn't
		// exist and the reconciler does not provision one.
		var s corev1.Secret
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-token-secret-dashboard-token", Namespace: namespace}, &s)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("rejects spec.dashboard.ingress.enabled with empty host at admission", func() {
		storageClass := "standard"
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-ingress", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{
					Enabled: true,
					Ingress: hermesv1alpha1.HermesAgentDashboardIngress{Enabled: true},
				},
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		// CRDV error format: the message comes from the CEL `message:` field;
		// the field path is the struct path, not the leaf field path the
		// webhook used to emit.
		Expect(err.Error()).To(ContainSubstring("dashboard.ingress.host is required when dashboard.ingress.enabled is true"))
	})

	It("cleans up dashboard Service + status.gateways when dashboard flips enabled→disabled", func() {
		// Bug C regression (smoke 2026-05-15): toggling dashboard.enabled
		// from true to false used to leave (a) the dashboard Service
		// dangling and (b) status.gateways[] + GatewaysReady condition
		// frozen at last-seen-good. Service must be deleted; gateways[]
		// must be cleared.
		originalFn := reconciler.ProbeHealthFn
		reconciler.ProbeHealthFn = func(_ context.Context, _ string) (*DashboardStatus, error) {
			return &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "running",
				GatewayPlatforms: map[string]PlatformState{
					"discord": {State: "connected"},
				},
			}, nil
		}
		DeferCleanup(func() { reconciler.ProbeHealthFn = originalFn })

		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "dashboard-toggle-off", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				Gateways: []hermesv1alpha1.HermesAgentGateway{
					{Type: "discord"},
				},
				Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Wait for the Service AND a populated status.gateways[] to confirm
		// we have something to clean up.
		svcName := "hermes-dashboard-toggle-off-dashboard"
		Eventually(func() bool {
			var svc corev1.Service
			return k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, &svc) == nil
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "dashboard-toggle-off", Namespace: namespace}, &got); err != nil {
				return false
			}
			return len(got.Status.Gateways) == 1
		}, timeout, interval).Should(BeTrue())

		// Flip Dashboard.Enabled to false.
		var refresh hermesv1alpha1.HermesAgent
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "dashboard-toggle-off", Namespace: namespace}, &refresh)).To(Succeed())
		refresh.Spec.Dashboard.Enabled = false
		Expect(k8sClient.Update(ctx, &refresh)).To(Succeed())

		// Service should be deleted.
		Eventually(func() bool {
			var svc corev1.Service
			err := k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: namespace}, &svc)
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())

		// status.gateways[] must be cleared AND the GatewaysReady condition
		// removed (or absent) — no stale snapshot.
		Eventually(func() bool {
			var got hermesv1alpha1.HermesAgent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "dashboard-toggle-off", Namespace: namespace}, &got); err != nil {
				return false
			}
			if len(got.Status.Gateways) != 0 {
				return false
			}
			for _, c := range got.Status.Conditions {
				if c.Type == hermesv1alpha1.ConditionGatewaysReady {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
	})

	It("does NOT create a dashboard Service when spec.dashboard.enabled is false", func() {
		storageClass := "standard"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "no-dashboard-svc-test", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())

		// Wait for the (non-dashboard) gateway service so we know reconcile ran.
		Eventually(func() error {
			var svc corev1.Service
			return k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-dashboard-svc-test", Namespace: namespace}, &svc)
		}, timeout, interval).Should(Succeed())

		var svc corev1.Service
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "hermes-no-dashboard-svc-test-dashboard", Namespace: namespace}, &svc)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("HermesAgent CRDV validation (Phase 11)", func() {
	// Proves the CRD-level x-kubernetes-validations rules reject bad CRs
	// before the webhook gets to see them. This locks in CRDV as the
	// validation layer before Phase 11B deletes the webhook.
	It("rejects CRs whose llmDefaultProvider does not match any llmProviders[].name", func() {
		storageClass := "standard"
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "crdv-bad-provider", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				LLMDefaultProvider: "anthropic",
				LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
					{Name: "deepseek"},
				},
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must match a name in spec.llmProviders"))
	})

	It("rejects CRs with empty spec.image via schema MinLength=1", func() {
		storageClass := "standard"
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "crdv-empty-image", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.image"))
	})

	It("rejects networkPolicy.enabled=true with no ingress and no egress (deny-all guard)", func() {
		storageClass := "standard"
		bad := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "crdv-denyall", Namespace: namespace},
			Spec: hermesv1alpha1.HermesAgentSpec{
				Image: "ghcr.io/example/hermes@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Storage: hermesv1alpha1.HermesAgentStorage{
					PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: &storageClass,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				},
				NetworkPolicy: hermesv1alpha1.HermesAgentNetworkPolicy{Enabled: true},
			},
		}
		err := k8sClient.Create(ctx, bad)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deny-all policy"))
	})
})
