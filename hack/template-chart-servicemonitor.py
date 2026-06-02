#!/usr/bin/env python3
# Copyright 2026 Undermountain Coding Company
# SPDX-License-Identifier: Apache-2.0

"""Wrap the kustomize-rendered ServiceMonitor in a Helm conditional.

The chart's `prometheus.serviceMonitor.enabled` value gates whether the
ServiceMonitor is rendered at install time. Users without Prometheus
Operator (monitoring.coreos.com/v1 CRDs) leave it false and the
ServiceMonitor never reaches the cluster — `helm install` succeeds even
on a barebones cluster.

The kustomize render of config/prometheus produces a single ServiceMonitor
manifest with metadata.namespace=system (the literal placeholder from the
kubebuilder scaffold). We:
  1. Rewrite metadata.namespace=system → metadata.namespace={{ .Release.Namespace }}
     so the ServiceMonitor lands in the operator's install namespace,
     wherever helm installs.
  2. Wrap the whole document in {{- if .Values.prometheus.serviceMonitor.enabled }}
     ... {{- end }}.

Idempotent. Reads input file path, writes output file path. Both required.
"""
from __future__ import annotations

import sys
from pathlib import Path


def rewrite(input_path: Path, output_path: Path) -> None:
    if not input_path.exists():
        print(f"template-chart-servicemonitor: input {input_path} missing", file=sys.stderr)
        sys.exit(2)

    content = input_path.read_text()
    # Rewrite the literal "namespace: system" → Release.Namespace.
    # Kustomize's `namespace:` directive isn't applied here because the
    # standalone config/prometheus kustomization doesn't set one — that's
    # intentional, so the Helm release controls placement instead.
    rewritten = content.replace(
        "namespace: system", "namespace: {{ .Release.Namespace }}"
    )

    lines = [
        "{{- if .Values.prometheus.serviceMonitor.enabled }}",
        rewritten.rstrip(),
        "{{- end }}",
        "",
    ]
    output_path.write_text("\n".join(lines))


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print("usage: template-chart-servicemonitor.py <input> <output>", file=sys.stderr)
        sys.exit(2)
    rewrite(Path(sys.argv[1]), Path(sys.argv[2]))
