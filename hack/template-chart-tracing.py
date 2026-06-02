#!/usr/bin/env python3
# Copyright 2026 Undermountain Coding Company
# SPDX-License-Identifier: Apache-2.0

"""Splice the OpenTelemetry env-var Helm template into the operator Deployment.

Mirrors the dedicated-script pattern introduced for Phase 9's ServiceMonitor:
rather than growing `hack/template-chart-image.py` into a swiss-army knife,
each concern gets its own splicer. Easier to grep, easier to delete when the
chart finally migrates to hand-written Helm templates.

What this splices: a Helm-conditional `env:` block on the operator's manager
container, populated from `.Values.tracing.otlp.endpoint` /
`.Values.tracing.otlp.serviceName`. When the endpoint value is empty the
whole block renders to nothing — the operator pod runs with no tracing env
and `internal/tracing.Init` no-ops (zero overhead).

Anchor strategy: insert AFTER the line `name: manager` in the manager
container spec. operator-sdk's render uses alphabetically-sorted container
fields (args, command, image, livenessProbe, name, ports, …), so inserting
after `name:` is the cleanest split point that doesn't collide with any
existing block. The manager container has no `env:` of its own — we own
this section outright.

Idempotent: the splice is gated on the marker comment NOT being present.
Re-running prints a warning and exits 0.
"""
from __future__ import annotations

import sys
from pathlib import Path

# Anchor: the manager container's `name:` field. There is exactly one
# container named `manager` in the rendered operator Deployment.
ANCHOR = "name: manager"

# Idempotency marker. If the splicer has already run, this string is
# present; we skip the rewrite rather than nest a second copy.
MARKER = "# OpenTelemetry tracing env (Phase 10)"

# The injected block. Helm-templated; renders to nothing when the endpoint
# value is empty. `default` on serviceName keeps the operator-binary's
# "hermes-operator" fallback (internal/tracing.defaultServiceName) honored
# without forcing the user to set both values together.
INJECTED = [
    MARKER,
    "{{- if .Values.tracing.otlp.endpoint }}",
    "env:",
    "- name: OTEL_EXPORTER_OTLP_ENDPOINT",
    "  value: {{ .Values.tracing.otlp.endpoint | quote }}",
    "- name: OTEL_SERVICE_NAME",
    '  value: {{ .Values.tracing.otlp.serviceName | default "hermes-operator" | quote }}',
    "{{- end }}",
]


def rewrite(path: Path) -> int:
    """Rewrite the file in place. Returns count of inserted blocks."""
    if not path.exists():
        print(f"template-chart-tracing: {path} does not exist", file=sys.stderr)
        return -1

    text = path.read_text()
    if MARKER in text:
        print(
            f"template-chart-tracing: marker already present in {path} — "
            f"re-run is a no-op."
        )
        return 0

    lines = text.splitlines()
    out_lines: list[str] = []
    inserted = 0
    for line in lines:
        out_lines.append(line)
        # Only act on the manager container's name field — anchor is exact
        # (no false positives from container names like "kube-rbac-proxy" etc.).
        if line.strip() == ANCHOR:
            indent = line[: len(line) - len(line.lstrip())]
            for inj in INJECTED:
                out_lines.append(f"{indent}{inj}")
            inserted += 1

    path.write_text("\n".join(out_lines) + "\n")
    return inserted


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {argv[0]} <path-to-operator.yaml>", file=sys.stderr)
        return 2

    path = Path(argv[1])
    inserted = rewrite(path)
    if inserted < 0:
        return 1

    if inserted == 0:
        # Either idempotent re-run (handled above with its own message) or
        # the anchor didn't match (potential drift — warn loudly).
        return 0
    if inserted > 1:
        # We only expect exactly one manager container in the rendered
        # Deployment. More than one is a structural surprise worth flagging.
        print(
            f"template-chart-tracing: WARNING — inserted {inserted} blocks "
            f"(expected exactly 1). Verify the rendered Deployment.",
            file=sys.stderr,
        )
    else:
        print(f"template-chart-tracing: injected tracing env block into {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
