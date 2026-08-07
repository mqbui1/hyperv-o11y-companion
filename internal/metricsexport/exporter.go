// Package metricsexport wraps the OpenTelemetry Go SDK's native OTLP/HTTP
// metric exporter (batching + retry built in) so neither service needs the
// hand-rolled JSON-chunking/backoff logic the PowerShell scripts use to work
// around the collector's shared sending_queue backpressure. Shared by both
// hyperv-scvmm-poller and hyperv-host-companion.
package metricsexport

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Exporter struct {
	provider *sdkmetric.MeterProvider
	Meter    metric.Meter
}

// New builds an OTLP/HTTP metric pipeline pointed at endpoint — for
// hyperv-scvmm-poller, the dedicated otlp/scvmm receiver on
// winsolarwinds2:4319; for hyperv-host-companion, the host-local Splunk OTel
// Collector (typically http://localhost:4318). serviceName identifies the
// emitting service in the exported resource. insecure disables TLS for
// plain-HTTP collector endpoints.
func New(ctx context.Context, serviceName, endpoint string, insecure bool) (*Exporter, error) {
	// otlpmetrichttp.WithEndpoint wants a bare host:port, not a full URL —
	// but every config file in this repo writes endpoint as
	// "http://host:port" for readability. Strip the scheme here so both
	// forms work.
	host := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(host)}
	if insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("building otlp exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("building resource: %w", err)
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(60*time.Second))),
	)
	return &Exporter{provider: provider, Meter: provider.Meter(serviceName)}, nil
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	return e.provider.Shutdown(ctx)
}
