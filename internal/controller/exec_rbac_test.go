// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func execAgent() *hermesv1alpha1.HermesAgent {
	return &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag1", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{ExecBackend: "kubernetes"},
	}
}

func TestDesiredExecRoleVerbs(t *testing.T) {
	role := desiredExecRole(execAgent())
	if role.Name != "hermes-ag1-exec" {
		t.Fatalf("role name = %q", role.Name)
	}
	// Single rule: pods + pods/exec + pods/log + pvc, no resourceNames pin
	// (session pod names are agent-chosen at runtime).
	wantRes := map[string]bool{
		"pods": false, "pods/exec": false, "pods/log": false,
		"persistentvolumeclaims": false,
	}
	for _, rule := range role.Rules {
		for _, r := range rule.Resources {
			if _, ok := wantRes[r]; ok {
				wantRes[r] = true
			}
		}
	}
	for res, seen := range wantRes {
		if !seen {
			t.Errorf("missing resource %q in exec Role", res)
		}
	}
}

func TestDesiredExecSessionSANoAutomount(t *testing.T) {
	sa := desiredExecSessionSA(execAgent())
	if sa.Name != "hermes-ag1-session" {
		t.Fatalf("session SA name = %q", sa.Name)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Errorf("session SA must set automountServiceAccountToken=false")
	}
}

func TestDesiredExecRoleBindingSubjectIsAgentSA(t *testing.T) {
	rb := desiredExecRoleBinding(execAgent())
	if rb.Subjects[0].Name != serviceAccountName(execAgent()) {
		t.Errorf("binding subject = %q, want agent SA", rb.Subjects[0].Name)
	}
	if rb.RoleRef.Name != "hermes-ag1-exec" {
		t.Errorf("binding roleRef = %q", rb.RoleRef.Name)
	}
}

func TestSetExecBackendReadyConditionSetsTrue(t *testing.T) {
	agent := execAgent()
	setExecBackendReadyCondition(agent)
	if !hasExecBackendReadyCondition(agent) {
		t.Fatal("expected ExecBackendReady condition after set")
	}
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionExecBackendReady {
			if c.Status != metav1.ConditionTrue {
				t.Errorf("ExecBackendReady status = %q, want True", c.Status)
			}
			if c.Reason != "KubernetesExecProvisioned" {
				t.Errorf("ExecBackendReady reason = %q, want KubernetesExecProvisioned", c.Reason)
			}
		}
	}
}

func TestClearExecBackendReadyConditionRemoves(t *testing.T) {
	agent := execAgent()
	setExecBackendReadyCondition(agent)
	clearExecBackendReadyCondition(agent)
	if hasExecBackendReadyCondition(agent) {
		t.Error("expected ExecBackendReady condition absent after clear")
	}
}

func TestClearExecBackendReadyConditionPreservesOthers(t *testing.T) {
	agent := execAgent()
	agent.Status.Conditions = []metav1.Condition{
		{Type: hermesv1alpha1.ConditionRBACSynced, Status: metav1.ConditionTrue, Reason: "Synced"},
		{Type: hermesv1alpha1.ConditionExecBackendReady, Status: metav1.ConditionTrue, Reason: "Provisioned"},
		{Type: hermesv1alpha1.ConditionPodReady, Status: metav1.ConditionFalse, Reason: "NotReady"},
	}
	clearExecBackendReadyCondition(agent)
	if hasExecBackendReadyCondition(agent) {
		t.Error("ExecBackendReady should be gone")
	}
	if len(agent.Status.Conditions) != 2 {
		t.Errorf("expected 2 remaining conditions, got %d", len(agent.Status.Conditions))
	}
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionExecBackendReady {
			t.Error("ExecBackendReady leaked through clear")
		}
	}
}

// minimalAgentSpec returns a minimal HermesAgentSpec good enough to pass the
// admission webhook (PVC + image required). Tests append fields as needed.
func execTestAgentSpec() hermesv1alpha1.HermesAgentSpec {
	storageClass := "standard"
	return hermesv1alpha1.HermesAgentSpec{
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
	}
}

var _ = Describe("HermesAgent exec-backend RBAC", func() {
	It("creates Role+RoleBinding+session SA when execBackend=kubernetes", func() {
		spec := execTestAgentSpec()
		spec.ExecBackend = "kubernetes"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "exec-on", Namespace: namespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

		// Role exists with the expected name + rules
		var role rbacv1.Role
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-on-exec", Namespace: namespace,
			}, &role)
		}, timeout, interval).Should(Succeed())

		// Role binds to the agent's SA, scoped to its namespace.
		var rb rbacv1.RoleBinding
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-on-exec", Namespace: namespace,
			}, &rb)
		}, timeout, interval).Should(Succeed())
		Expect(rb.RoleRef.Kind).To(Equal("Role"))
		Expect(rb.RoleRef.Name).To(Equal("hermes-exec-on-exec"))
		Expect(rb.Subjects).To(HaveLen(1))
		Expect(rb.Subjects[0].Name).To(Equal("hermes-exec-on"))

		// Session SA exists with automount=false (powerless identity).
		var sa corev1.ServiceAccount
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-on-session", Namespace: namespace,
			}, &sa)
		}, timeout, interval).Should(Succeed())
		Expect(sa.AutomountServiceAccountToken).NotTo(BeNil())
		Expect(*sa.AutomountServiceAccountToken).To(BeFalse())

		// CR ownerRef set (so CR delete cascades via GC).
		Expect(role.OwnerReferences).To(HaveLen(1))
		Expect(role.OwnerReferences[0].Name).To(Equal("exec-on"))
		Expect(rb.OwnerReferences).To(HaveLen(1))
		Expect(rb.OwnerReferences[0].Name).To(Equal("exec-on"))
		Expect(sa.OwnerReferences).To(HaveLen(1))
		Expect(sa.OwnerReferences[0].Name).To(Equal("exec-on"))
	})

	It("deletes them when execBackend switches back to local", func() {
		spec := execTestAgentSpec()
		spec.ExecBackend = "kubernetes"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "exec-off", Namespace: namespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-off-exec", Namespace: namespace,
			}, &rbacv1.Role{})
		}, timeout, interval).Should(Succeed())

		// Flip to local — reconcile should GC the exec RBAC.
		patch := client.MergeFrom(agent.DeepCopy())
		agent.Spec.ExecBackend = "local"
		Expect(k8sClient.Patch(ctx, agent, patch)).To(Succeed())

		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-off-exec", Namespace: namespace,
			}, &rbacv1.Role{})
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "exec Role should be deleted after toggle back to local")

		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-off-exec", Namespace: namespace,
			}, &rbacv1.RoleBinding{})
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "exec RoleBinding should be deleted after toggle back to local")

		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-exec-off-session", Namespace: namespace,
			}, &corev1.ServiceAccount{})
			return apierrors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "exec session SA should be deleted after toggle back to local")
	})

	It("does NOT create exec RBAC when spec.serviceAccountName is user-provided (BYO opt-out)", func() {
		// Mirrors the self-RBAC BYO opt-out (Phase 10.6, ee710c0): BYO SAs may be
		// shared across multiple HermesAgent CRs, so layering operator-managed
		// grants onto them would create cross-agent leaks. Skip the exec RBAC.
		externalSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-exec-external-sa", Namespace: namespace},
		}
		Expect(k8sClient.Create(ctx, externalSA)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, externalSA) })

		spec := execTestAgentSpec()
		spec.ExecBackend = "kubernetes"
		spec.ServiceAccountName = "byo-exec-external-sa"
		agent := &hermesv1alpha1.HermesAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "byo-exec", Namespace: namespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

		// Wait for reconcile to finish a cycle (Deployment landing is the same
		// proxy the self-RBAC BYO test uses; PVC name == agent name doesn't
		// hold for the operator's helper, and the deployment exists once the
		// secrets gate + reconcile sequence has run end-to-end).
		Eventually(func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "hermes-byo-exec", Namespace: namespace,
			}, &dep)
		}, timeout, interval).Should(Succeed())

		// Exec Role + RoleBinding + session SA must NOT exist.
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name: "hermes-byo-exec-exec", Namespace: namespace,
		}, &rbacv1.Role{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "exec Role should NOT exist for BYO-SA agents")

		err = k8sClient.Get(ctx, types.NamespacedName{
			Name: "hermes-byo-exec-exec", Namespace: namespace,
		}, &rbacv1.RoleBinding{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "exec RoleBinding should NOT exist for BYO-SA agents")

		err = k8sClient.Get(ctx, types.NamespacedName{
			Name: "hermes-byo-exec-session", Namespace: namespace,
		}, &corev1.ServiceAccount{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "exec session SA should NOT exist for BYO-SA agents")
	})
})
