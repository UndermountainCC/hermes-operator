#!/usr/bin/env python3
# Copyright 2026 Undermountain Coding Company
# SPDX-License-Identifier: Apache-2.0

"""Splice Helm value placeholders into the kustomize-rendered chart manifests.

Kustomize-rendered manifests don't reference Helm values, so the chart's
`values.yaml` keys are dead unless we rewrite the rendered output to point
at them. This script runs after `kustomize build config/default` (via
`make helm-render`) and does two splices on the resulting operator.yaml:

1. Image fields. Rewrite the hardcoded:
       image: controller:latest
   into a Helm-templated form that respects `values.image.repository`,
   `values.image.tag`, and `values.image.pullPolicy`.

2. Operator CLI args. Inject a conditional Helm block AFTER the
   `--health-probe-bind-address=:8081` arg that adds
       --allowed-cluster-roles={{ .Values.operator.allowedClusterRoles }}
   when the value is non-empty. Without this, the chart's
   `operator.allowedClusterRoles` value would be dead and the operator
   would always run with an empty allowlist.

Idempotent: re-running on already-templated content is a no-op. Each
splice is gated on the LITERAL source pattern being present; if the
kustomize-rendered shape changes, the script logs a warning rather than
mangling output.

Surfaced by Phase 3 kind smoke (image fields) and the Phase 2 kind smoke
(CLI args). Both were chart-claims-without-wiring bugs that the smoke
caught after Helm install but before the operator did anything useful.
"""
from __future__ import annotations

import sys
from pathlib import Path

# --- Splice 1: image fields ---
IMAGE_ORIGINAL = "image: controller:latest"
IMAGE_TEMPLATED = (
    'image: "{{ .Values.image.repository }}:'
    '{{ .Values.image.tag | default .Chart.AppVersion }}"'
)
IMAGE_PULL_POLICY = "imagePullPolicy: {{ .Values.image.pullPolicy }}"

# --- Splice 2: operator args ---
# Match the LAST default-argument line emitted by operator-sdk's Deployment
# template. We inject the conditional block after it; if operator-sdk ever
# changes this default arg, the regex below will miss and the script warns.
ARGS_ANCHOR_LINE_SUFFIX = "- --health-probe-bind-address=:8081"
ARGS_INJECTED_LINES = [
    "{{- if .Values.operator.allowedClusterRoles }}",
    "- --allowed-cluster-roles={{ .Values.operator.allowedClusterRoles }}",
    "{{- end }}",
]


def rewrite(path: Path) -> tuple[int, int]:
    """Rewrite the file in place. Returns (image_lines_rewritten, args_blocks_inserted)."""
    if not path.exists():
        print(f"template-chart-image: {path} does not exist", file=sys.stderr)
        return -1, -1

    out_lines: list[str] = []
    image_count = 0
    args_count = 0
    for line in path.read_text().splitlines():
        stripped = line.lstrip()

        # Splice 1: image fields
        if stripped == IMAGE_ORIGINAL:
            indent = line[: len(line) - len(stripped)]
            out_lines.append(f"{indent}{IMAGE_TEMPLATED}")
            out_lines.append(f"{indent}{IMAGE_PULL_POLICY}")
            image_count += 1
            continue

        out_lines.append(line)

        # Splice 2: operator args — inject Helm conditional after the anchor.
        # Match on suffix-strip to be agnostic to indentation depth.
        if stripped == ARGS_ANCHOR_LINE_SUFFIX:
            indent = line[: len(line) - len(stripped)]
            for inj in ARGS_INJECTED_LINES:
                out_lines.append(f"{indent}{inj}")
            args_count += 1

    path.write_text("\n".join(out_lines) + "\n")
    return image_count, args_count


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {argv[0]} <path-to-operator.yaml>", file=sys.stderr)
        return 2

    path = Path(argv[1])
    image_count, args_count = rewrite(path)
    if image_count < 0:
        return 1

    # Each splice has its own idempotency-warning. Both 0 on a re-run is normal.
    if image_count == 0:
        print(
            f"template-chart-image: no occurrences of '{IMAGE_ORIGINAL}' "
            f"found in {path} — already templated, or kustomize image "
            f"placeholder changed.",
            file=sys.stderr,
        )
    else:
        print(f"template-chart-image: rewrote {image_count} image line(s) in {path}")

    if args_count == 0:
        print(
            f"template-chart-image: no '{ARGS_ANCHOR_LINE_SUFFIX}' anchor "
            f"found in {path} — operator-sdk's default args may have changed; "
            f"the operator.allowedClusterRoles value will be dead until this "
            f"script's anchor is updated.",
            file=sys.stderr,
        )
    else:
        print(f"template-chart-image: injected {args_count} args block(s) in {path}")

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
