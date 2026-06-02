// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredDeployment_SuspendZeroesReplicas(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent2", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{Suspend: true},
	}
	d := desiredDeployment(agent)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Errorf("suspended: want replicas=0, got %v", d.Spec.Replicas)
	}

	agent.Spec.Suspend = false
	d = desiredDeployment(agent)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("not suspended: want replicas=1, got %v", d.Spec.Replicas)
	}
}

func TestDesiredDeployment_BasicShape(t *testing.T) {
	storageClass := "local-path"
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image:           "registry/hermes@sha256:abc",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			Storage: hermesv1alpha1.HermesAgentStorage{
				PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &storageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
					},
				},
			},
		},
	}

	dep := desiredDeployment(agent)

	if dep.Name != "hermes-agent1" {
		t.Errorf("name: want hermes-agent1, got %q", dep.Name)
	}
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("strategy: want Recreate, got %v", dep.Spec.Strategy.Type)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
		t.Errorf("replicas: want 1, got %v", dep.Spec.Replicas)
	}

	pod := dep.Spec.Template.Spec
	if len(pod.Containers) != 1 {
		t.Fatalf("containers: want 1, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]

	if c.Name != "hermes" {
		t.Errorf("container name: want hermes, got %q", c.Name)
	}
	if c.Image != "registry/hermes@sha256:abc" {
		t.Errorf("image: want registry/hermes@sha256:abc, got %q", c.Image)
	}
	if c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("imagePullPolicy: want IfNotPresent, got %v", c.ImagePullPolicy)
	}
	if len(c.Args) != 2 || c.Args[0] != "gateway" || c.Args[1] != "run" {
		t.Errorf("args: want [gateway run], got %v", c.Args)
	}

	// Volume mount /opt/data ← PVC
	foundMount := false
	for _, m := range c.VolumeMounts {
		if m.MountPath == "/opt/data" {
			foundMount = true
			if m.Name != "data" {
				t.Errorf("data volume mount name: want data, got %q", m.Name)
			}
		}
	}
	if !foundMount {
		t.Errorf("missing volume mount at /opt/data")
	}

	// PVC volume references our PVC
	foundVol := false
	for _, v := range pod.Volumes {
		if v.Name == "data" && v.PersistentVolumeClaim != nil &&
			v.PersistentVolumeClaim.ClaimName == "hermes-agent1-data" {
			foundVol = true
		}
	}
	if !foundVol {
		t.Errorf("missing data volume backed by hermes-agent1-data")
	}

	// Service account name
	if pod.ServiceAccountName != "hermes-agent1" {
		t.Errorf("serviceAccountName: want hermes-agent1, got %q", pod.ServiceAccountName)
	}

	// Env composition — operator-stamped HERMES_PROFILE must be present.
	if idx := indexByName(c.Env, "HERMES_PROFILE"); idx == -1 {
		t.Errorf("container env missing HERMES_PROFILE")
	}
}

func TestDesiredDeployment_TerminationGracePeriodSeconds(t *testing.T) {
	storageClass := "local-path"
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image: "registry/hermes@sha256:abc",
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

	dep := desiredDeployment(agent)

	// 180s drain budget (upstream agent.restart_drain_timeout default) + 30s
	// signal-forwarding/teardown buffer. SIGKILL mid-drain abandons in-flight
	// turns; this gives the gateway room to runner.stop() cleanly.
	tgs := dep.Spec.Template.Spec.TerminationGracePeriodSeconds
	if tgs == nil {
		t.Fatalf("terminationGracePeriodSeconds: want set, got nil")
	}
	if *tgs != 210 {
		t.Errorf("terminationGracePeriodSeconds: want 210, got %d", *tgs)
	}
}

func TestDesiredDeployment_PassesNodePlacement(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image:        "registry/hermes@sha256:abc",
			NodeSelector: map[string]string{"zone": "example-zone"},
			Tolerations: []corev1.Toleration{
				{Key: "zone", Operator: corev1.TolerationOpEqual, Value: "example-zone", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}

	dep := desiredDeployment(agent)
	pod := dep.Spec.Template.Spec

	if pod.NodeSelector["zone"] != "example-zone" {
		t.Errorf("nodeSelector zone: want example-zone, got %q", pod.NodeSelector["zone"])
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "zone" {
		t.Errorf("tolerations: want one with key=zone, got %v", pod.Tolerations)
	}
}
