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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// tracerName is the global instrumentation library name. Stable across
// releases; downstream trace queries (`instrumentation.name = "hermes-operator"`)
// rely on it. Same string is also used as the default service.name resource
// attribute by internal/tracing — keeping them aligned is intentional, so
// span filters and resource filters in a tracing UI hit the same name.
const tracerName = "hermes-operator"

// tracer returns the package-scoped Tracer. The global TracerProvider is
// installed in cmd/main.go via internal/tracing.Init. When tracing is
// disabled (OTEL_EXPORTER_OTLP_ENDPOINT unset), this returns the SDK's
// no-op tracer — Start/End become cheap nops, attribute setters are
// dropped without allocating.
func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// startSpan opens a span on ctx with the standard agent name/namespace
// attributes pre-populated. The caller MUST defer span.End() and is
// responsible for recording errors via endSpan.
//
// Centralising the attribute set in one place means every reconcile-side
// span carries the same shape — Jaeger/Tempo filter expressions only need
// to know hermesagent.name + hermesagent.namespace.
func startSpan(ctx context.Context, name string, agent *hermesv1alpha1.HermesAgent) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, name)
	span.SetAttributes(
		attribute.String("hermesagent.name", agent.Name),
		attribute.String("hermesagent.namespace", agent.Namespace),
	)
	return ctx, span
}

// endSpan finalizes a span. On error, records the error and sets status=Error
// so tracing UIs flag the span red and surface the message in the span
// detail panel. On nil error, sets status=Ok so the UI distinguishes
// "succeeded" from "still running / unknown".
//
// Designed to be used as `defer endSpan(span, err)` AFTER the function body
// — Go's defer evaluates the err argument at the deferred call site, not at
// the defer statement, when err is captured via pointer. Since we use
// closures throughout to set the named return / outer error variable, the
// pattern at call sites is:
//
//	defer func() { endSpan(span, err) }()
//
// where err is the named return of the wrapping function. See the
// reconcileXxx implementations.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}
