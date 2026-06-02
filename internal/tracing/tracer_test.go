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

package tracing

import (
	"context"
	"testing"
	"time"
)

// TestInitDisabled: zero-config posture. With OTEL_EXPORTER_OTLP_ENDPOINT
// unset, Init must return a no-op shutdown and no error. The operator runs
// anywhere without an OTLP backend; this is the most-trodden code path.
func TestInitDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("Init() returned nil shutdown; want non-nil no-op")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}
}

// TestInitMalformedEndpoint: a malformed endpoint must fail fast at startup.
// Operator-config bugs deserve a clear error, not silently-dropped spans for
// the pod's lifetime.
//
// otlptracegrpc.WithEndpointURL parses the value as a URL — anything that
// doesn't parse returns an error from New(). We use a value with invalid
// URL syntax to exercise that path without relying on DNS / network state.
func TestInitMalformedEndpoint(t *testing.T) {
	// "://no-scheme" trips url.Parse because the scheme is empty. Validated
	// locally that otlptracegrpc.WithEndpointURL surfaces this as an error.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "://no-scheme")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Init(ctx)
	if err == nil {
		t.Fatal("Init() with malformed endpoint returned nil error; want error")
	}
}

// TestInitDefaultServiceName: the default service.name is applied when
// OTEL_SERVICE_NAME is unset. We can't easily inspect the resource via the
// public API without standing up a fake exporter, so this test only asserts
// the constant value used in the package is the documented default — a
// guard against accidental rename of the contract.
func TestInitDefaultServiceName(t *testing.T) {
	if defaultServiceName != "hermes-operator" {
		t.Errorf("defaultServiceName = %q, want %q (CHANGELOG + docs depend on this)", defaultServiceName, "hermes-operator")
	}
}
