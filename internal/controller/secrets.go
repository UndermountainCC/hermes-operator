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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// SecretRef identifies a Secret+key the operator must verify before Pod
// creation. Key="" means the entire Secret is consumed (envFrom).
type SecretRef struct {
	SecretName string
	Key        string
}

// collectSecretRefs walks every env / envFrom in the spec and returns the
// deduped set of Secret references that must exist for the Pod to start.
func collectSecretRefs(agent *hermesv1alpha1.HermesAgent) []SecretRef {
	seen := map[SecretRef]struct{}{}
	addEnv := func(envs []corev1.EnvVar) {
		for _, e := range envs {
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				continue
			}
			seen[SecretRef{SecretName: e.ValueFrom.SecretKeyRef.Name, Key: e.ValueFrom.SecretKeyRef.Key}] = struct{}{}
		}
	}
	for _, p := range agent.Spec.LLMProviders {
		addEnv(p.Env)
	}
	for _, g := range agent.Spec.Gateways {
		addEnv(g.Env)
	}
	addEnv(agent.Spec.Env)

	for _, ef := range agent.Spec.EnvFrom {
		if ef.SecretRef != nil {
			seen[SecretRef{SecretName: ef.SecretRef.Name, Key: ""}] = struct{}{}
		}
	}

	out := make([]SecretRef, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	return out
}

// validateSecretRefs returns nil iff every Secret referenced in the spec
// exists AND, when a key is specified, the Secret has that key.
func (r *HermesAgentReconciler) validateSecretRefs(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.SecretsValidation", agent)
	defer func() { endSpan(span, err) }()

	refs := collectSecretRefs(agent)
	for _, ref := range refs {
		s := &corev1.Secret{}
		key := types.NamespacedName{Name: ref.SecretName, Namespace: agent.Namespace}
		err := r.Get(ctx, key, s)
		switch {
		case apierrors.IsNotFound(err):
			return fmt.Errorf("secret %s/%s not found", agent.Namespace, ref.SecretName)
		case err != nil:
			return fmt.Errorf("get Secret %s/%s: %w", agent.Namespace, ref.SecretName, err)
		}
		if ref.Key == "" {
			continue
		}
		if _, ok := s.Data[ref.Key]; !ok {
			return fmt.Errorf("secret %s/%s missing key %q", agent.Namespace, ref.SecretName, ref.Key)
		}
	}
	return nil
}
