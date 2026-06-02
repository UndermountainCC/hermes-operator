// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestPVCName_HonorsExistingClaimName(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent2", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Storage: hermesv1alpha1.HermesAgentStorage{ExistingClaimName: "hermes-data"},
		},
	}
	if got := pvcName(agent); got != "hermes-data" {
		t.Errorf("pvcName with ExistingClaimName: want hermes-data, got %q", got)
	}

	agent2 := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent2", Namespace: "hermes"},
	}
	if got := pvcName(agent2); got != "hermes-agent2-data" {
		t.Errorf("pvcName without ExistingClaimName: want hermes-agent2-data, got %q", got)
	}
}

func TestDesiredPVC_HonorsSpec(t *testing.T) {
	storageClass := "local-path"
	qty := resource.MustParse("100Gi")
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Storage: hermesv1alpha1.HermesAgentStorage{
				PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &storageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
					},
				},
			},
		},
	}

	pvc := desiredPVC(agent)

	if pvc.Name != "hermes-agent1-data" {
		t.Errorf("name: want hermes-agent1-data, got %q", pvc.Name)
	}
	if pvc.Namespace != "hermes" {
		t.Errorf("namespace: want hermes, got %q", pvc.Namespace)
	}
	if got := pvc.Labels["hermes.undermountain.cc/agent"]; got != "agent1" {
		t.Errorf("label hermes.undermountain.cc/agent: want agent1, got %q", got)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("accessModes: want [ReadWriteOnce], got %v", pvc.Spec.AccessModes)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "local-path" {
		t.Errorf("storageClassName: want local-path, got %v", pvc.Spec.StorageClassName)
	}
}
