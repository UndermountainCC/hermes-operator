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

// Package tracing wires the operator into OpenTelemetry tracing.
//
// Bootstrapping is gated entirely on the standard OTel env vars:
//
//   - OTEL_EXPORTER_OTLP_ENDPOINT — when unset, Init is a no-op (the global
//     tracer provider stays as the SDK default no-op, every Start/End is
//     cheap, and the operator runs anywhere without an OTLP backend).
//   - OTEL_SERVICE_NAME — defaults to "hermes-operator" when unset; emitted
//     as the service.name resource attribute on every span.
//   - OTEL_RESOURCE_ATTRIBUTES — additional resource attributes (parsed by
//     the OTel SDK's resource.Default()).
//
// Why OTLP gRPC only: the modern OTel ecosystem (OTel Collector, Jaeger via
// the collector, Tempo, Honeycomb, Grafana Cloud) all speak OTLP. Adding HTTP
// would double the surface for no real-world value. If a user runs a Jaeger
// instance that doesn't speak OTLP, the workaround is the OTel Collector,
// not a second exporter here.
//
// Why no auto-degrade on a bad endpoint: a malformed endpoint at boot is an
// operator-config bug, not a transient runtime condition. Failing loud at
// startup beats silently dropping every span for the lifetime of the pod.
package tracing

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	ctrl "sigs.k8s.io/controller-runtime"
)

// defaultServiceName is the service.name resource attribute applied when
// OTEL_SERVICE_NAME is unset. Stable across releases; downstream trace
// queries (`service.name = "hermes-operator"`) rely on it.
const defaultServiceName = "hermes-operator"

// initTimeout caps the OTLP gRPC connection setup. Without a deadline a
// misconfigured endpoint (DNS doesn't resolve, port closed) blocks Init
// indefinitely and the operator pod never reaches /readyz. 10s is generous
// enough for in-cluster routing to settle but short enough that CrashLoopBackOff
// surfaces a configuration error within a couple of minutes.
const initTimeout = 10 * time.Second

// noopShutdown is the shutdown function returned when tracing is disabled.
// Kept as a package-level value so the cmd-side defer pattern is identical
// whether or not tracing wires up.
func noopShutdown(_ context.Context) error { return nil }

// Init wires the global OpenTelemetry TracerProvider.
//
// Returns a shutdown function the caller MUST defer — when tracing is on,
// it flushes the batch span processor and closes the OTLP gRPC connection.
// When tracing is off, it is a no-op.
//
// Behavior:
//   - OTEL_EXPORTER_OTLP_ENDPOINT unset → no-op + nil err (zero overhead).
//   - OTEL_EXPORTER_OTLP_ENDPOINT set → OTLP gRPC exporter + batch processor +
//     TracerProvider set as global. Any failure during setup (malformed
//     endpoint, DNS hard-fail before the 10s deadline) returns the error so
//     the caller fails fast at startup.
func Init(ctx context.Context) (func(context.Context) error, error) {
	log := ctrl.Log.WithName("tracing")
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		log.Info("OpenTelemetry: OTEL_EXPORTER_OTLP_ENDPOINT not set; tracing disabled")
		return noopShutdown, nil
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	// Validate the endpoint URL up front. otlptracegrpc.New silently logs
	// (rather than returns) parse failures via its internal logger and
	// happily constructs a "ready" exporter that never actually exports —
	// exactly the opposite of the fail-fast contract we want at startup.
	// Parse here ourselves so the returned error has somewhere to land.
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("parse OTEL_EXPORTER_OTLP_ENDPOINT=%q: %w", endpoint, err)
	}

	// Bound the gRPC dial. Without this, a misconfigured endpoint blocks
	// startup indefinitely — the operator pod never reaches /readyz and
	// CrashLoopBackOff hides the underlying cause.
	dialCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	// otlptracegrpc.New honors OTEL_EXPORTER_OTLP_ENDPOINT internally; the
	// explicit WithEndpointURL keeps the code path obvious.
	exp, err := otlptracegrpc.New(
		dialCtx,
		otlptracegrpc.WithEndpointURL(endpoint),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP gRPC exporter (endpoint=%q): %w", endpoint, err)
	}

	// resource.Merge so OTEL_RESOURCE_ATTRIBUTES (parsed by resource.Default)
	// composes with our explicit service.name. resource.Default also injects
	// telemetry.sdk.* attributes — leave them.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		_ = exp.Shutdown(ctx)
		return nil, fmt.Errorf("build OTel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	log.Info("OpenTelemetry tracing enabled", "endpoint", endpoint, "service.name", serviceName)

	// Compose shutdown: flush the batch processor (via tp.Shutdown) which in
	// turn closes the exporter. Use the caller's ctx so a hung backend
	// during pod termination can't stall SIGTERM forever — the caller is
	// expected to pass a deadline.
	return func(shutdownCtx context.Context) error {
		return tp.Shutdown(shutdownCtx)
	}, nil
}
