/*
Copyright 2025 The Crossplane Authors.

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

// Package tracing wires up an optional OpenTelemetry tracer provider. It is a
// no-op unless an OTLP endpoint is configured via the standard OTEL_* env vars,
// so spans emitted on the global tracer fall back to a no-op tracer by default
// and start exporting once an endpoint is set.
package tracing

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ServiceName is the OpenTelemetry service.name reported by the provider.
const ServiceName = "provider-kubeconfig"

// enabled reports whether any OTLP traces endpoint is configured.
func enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Setup installs a global OTLP tracer provider when an OTEL endpoint is set.
// When no endpoint is configured it returns a no-op shutdown func and on=false,
// leaving the global no-op tracer in place. The returned shutdown func flushes
// and stops the exporter and should be deferred by the caller.
func Setup(ctx context.Context, serviceVersion string) (shutdown func(context.Context) error, on bool, err error) {
	noop := func(context.Context) error { return nil }
	if !enabled() {
		return noop, false, nil
	}

	// otlptracegrpc reads endpoint/headers/TLS from the standard OTEL_* env vars.
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, false, err
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", ServiceName),
		attribute.String("service.version", serviceVersion),
	))
	if err != nil {
		return noop, false, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, true, nil
}
