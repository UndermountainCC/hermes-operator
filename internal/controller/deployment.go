// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// desiredDeployment returns the Deployment the operator wants to exist
// for this agent.
//
// Replicas pinned to 1: gateway holds an fcntl(LOCK_EX|LOCK_NB) on
// $HERMES_HOME/gateway.lock — a second hermes process crashloops with
// "Gateway already running (PID …)". Strategy=Recreate ensures the old
// pod terminates (releases the flock) before the new pod starts, instead
// of the RollingUpdate default which briefly runs two pods. Empirically
// validated by the co-run spike (2026-05-15).
func desiredDeployment(agent *hermesv1alpha1.HermesAgent) *appsv1.Deployment {
	labels := agentLabels(agent)
	// Replicas: 1 normally; 0 when suspended. The flock invariant is about
	// never running TWO gateway processes (they contend on gateway.lock) —
	// zero is always safe.
	replicas := int32(1)
	if agent.Spec.Suspend {
		replicas = 0
	}
	defaultShmSize := resource.MustParse("1Gi")

	// Probe shape per kind-spike findings (2026-05-15): hermes gateway status
	// is fast (~200ms) but always exits 0 — we have to grep for the literal
	// "✓ Gateway is running" line and let bash synthesize the exit code. The
	// hermes binary is at /opt/hermes/.venv/bin/hermes; it is NOT on the
	// container's default $PATH because the upstream entrypoint doesn't run
	// when k8s exec invokes the probe. The ✓ glyph is U+2713 (multi-byte
	// UTF-8); the container ships an English locale so grep handles it
	// fine — if that ever changes, the probe will silently miss.
	probeCmd := []string{
		"/bin/bash", "-c",
		"/opt/hermes/.venv/bin/hermes gateway status | grep -q '✓ Gateway is running'",
	}

	container := corev1.Container{
		Name:            "hermes",
		Image:           agent.Spec.Image,
		ImagePullPolicy: agent.Spec.ImagePullPolicy,
		Args:            []string{"gateway", "run"},
		Env:             renderEnv(agent),
		EnvFrom:         agent.Spec.EnvFrom,
		Resources:       agent.Spec.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: "/opt/data"},
			{Name: "dshm", MountPath: "/dev/shm"},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: probeCmd},
			},
			PeriodSeconds:    10,
			TimeoutSeconds:   3,
			FailureThreshold: 3,
			SuccessThreshold: 1,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: probeCmd},
			},
			PeriodSeconds:    30,
			TimeoutSeconds:   5,
			FailureThreshold: 5,
			SuccessThreshold: 1,
			// Avoid SIGKILL during the early startup window — gateway can take
			// up to ~10s to initialize on first boot.
			InitialDelaySeconds: 20,
		},
	}

	containers := []corev1.Container{container}

	// Optional dashboard sidecar. The sidecar shares /opt/data with the gateway
	// (co-run safety validated by spike a549da47, 2026-05-15: dashboard takes
	// no fcntl locks; gateway's lock is gateway-vs-gateway only). The Command
	// slice is hardcoded — never passes --tui which would spawn a second
	// hermes agent process touching $HERMES_HOME.
	//
	// Auth model (verified 2026-05-15 against upstream v2026.4.30): upstream
	// generates an ephemeral _SESSION_TOKEN at process start and injects it
	// into the rendered SPA HTML. There is no env-var hook for an externally-
	// supplied token, and /api/status is unconditionally unauthenticated. The
	// operator does NOT provision a token Secret. External exposure auth is
	// the user's responsibility, via Ingress annotations (nginx auth-url,
	// oauth2-proxy fronting, etc.).
	if agent.Spec.Dashboard.Enabled {
		dashImage := agent.Spec.Dashboard.Image
		if dashImage == "" {
			dashImage = agent.Spec.Image
		}
		dashboardContainer := corev1.Container{
			Name:  "dashboard",
			Image: dashImage,
			Command: []string{
				"/opt/hermes/.venv/bin/hermes",
				"dashboard",
				"--insecure",
				"--host", "0.0.0.0",
				"--no-open",
			},
			Ports: []corev1.ContainerPort{
				{Name: "dashboard", ContainerPort: 9119, Protocol: corev1.ProtocolTCP},
			},
			Resources: agent.Spec.Dashboard.Resources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "data", MountPath: "/opt/data"},
			},
			// Upstream image's hermes user is UID 10000 (useradd in
			// nousresearch/hermes-agent's Dockerfile). The Command override
			// above bypasses the image entrypoint that would have gosu-dropped
			// to this UID, so we set it explicitly. Otherwise the dashboard
			// writes /opt/data/* as root and the gateway container (also
			// UID 10000, via its image entrypoint) cannot read or write
			// /opt/data/logs/agent.log, /opt/data/state.db, etc. Caught by
			// real-cluster smoke 2026-05-15.
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:  ptr.To(int64(10000)),
				RunAsGroup: ptr.To(int64(10000)),
			},
		}
		containers = append(containers, dashboardContainer)
	}

	// shareProcessNamespace: true puts all containers in this pod into a single
	// shared Linux PID namespace. Upstream's hermes dashboard uses PID-based
	// gateway-liveness detection (https://hermes-agent.nousresearch.com/docs/user-guide/docker)
	// and won't see the gateway's PID without this. Default K8s containers
	// have isolated PID namespaces. Cost: containers in this pod can see each
	// other's process trees — fine for a single-tenant agent pod.
	var shareProcessNamespace *bool
	if agent.Spec.Dashboard.Enabled {
		shareProcessNamespace = ptr.To(true)
	}

	// terminationGracePeriodSeconds: K8s sends SIGTERM to tini (PID 1) at pod
	// termination; tini forwards (in -g mode) to the gateway process group.
	// The gateway then runs runner.stop() which drains active turns up to
	// agent.restart_drain_timeout (default 180s in upstream
	// hermes_cli/config.py DEFAULT_CONFIG; see
	// gateway/restart.py::DEFAULT_GATEWAY_RESTART_DRAIN_TIMEOUT) before
	// closing SessionDB and releasing the gateway.lock flock. K8s's default
	// 30s grace period SIGKILLs the pod mid-drain → in-flight agent turns
	// are abandoned, the .clean_shutdown marker is never written, and
	// on next boot the session store calls suspend_recently_active().
	//
	// We pin 210s = 180s drain budget + 30s buffer for K8s + tini +
	// our-entrypoint signal forwarding and the post-drain DB/lock teardown.
	// Raise via a future spec.terminationGracePeriodSeconds knob if a
	// deployment runs very-long-reasoning models and bumps
	// agent.restart_drain_timeout in config.yaml.
	terminationGracePeriodSeconds := int64(210)

	podSpec := corev1.PodSpec{
		ServiceAccountName:            serviceAccountName(agent),
		NodeSelector:                  agent.Spec.NodeSelector,
		Tolerations:                   agent.Spec.Tolerations,
		Affinity:                      agent.Spec.Affinity,
		ImagePullSecrets:              agent.Spec.ImagePullSecrets,
		ShareProcessNamespace:         shareProcessNamespace,
		TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
		Containers:                    containers,
		Volumes: []corev1.Volume{
			{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName(agent),
					},
				},
			},
			{
				Name: "dshm",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium:    corev1.StorageMediumMemory,
						SizeLimit: &defaultShmSize,
					},
				},
			},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName(agent),
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app":                              "hermes",
				"hermes.undermountain.cc/agent":    agent.Name,
				"hermes.undermountain.cc/agent-ns": agent.Namespace,
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// reconcileDeployment ensures the agent's Deployment exists and matches
// the desired shape. Server-side apply means the operator owns exactly the
// fields it declares on the desired object; fields managed by other parties
// (notably Deployment.status, owned by kube-controller-manager) are
// preserved without conflict. Pod template changes trigger a Recreate
// rollout (strategy.Type=Recreate, replicas=1).
func (r *HermesAgentReconciler) reconcileDeployment(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.Deployment", agent)
	defer func() { endSpan(span, err) }()

	return r.applyObject(ctx, agent, desiredDeployment(agent))
}
