// Command otlp-emitter produces a steady trickle of spans, metrics and log
// lines aimed at a collector's OTLP/HTTP receiver. It exists so the collector
// tier of the end-to-end suite can verify each pipeline without depending on
// auto-instrumentation, which is a separate thing under test.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func main() {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		log.Fatal("OTEL_EXPORTER_OTLP_ENDPOINT is required")
	}
	service := os.Getenv("OTEL_SERVICE_NAME")
	if service == "" {
		service = "otlp-emitter"
	}
	if err := emit(endpoint, service, 2*time.Second, make(chan struct{})); err != nil {
		log.Fatal(err)
	}
}

// shutdownTimeout bounds the deferred provider Shutdown calls so that
// shutting down against an unreachable endpoint cannot stall indefinitely.
const shutdownTimeout = 5 * time.Second

// emit ships one span, one metric datapoint and one log line per tick until
// stop is closed. Export failures (e.g. the collector isn't up yet) are
// logged by the SDK's error handler and are not fatal, since in the real
// suite this emitter starts before the collector tier is ready.
func emit(endpoint, service string, interval time.Duration, stop <-chan struct{}) error {
	ctx := context.Background()

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(service),
	))
	if err != nil {
		return fmt.Errorf("build resource: %w", err)
	}

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithInsecure())
	if err != nil {
		return fmt.Errorf("trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter, sdktrace.WithBatchTimeout(interval)),
		sdktrace.WithResource(res))
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = tracerProvider.Shutdown(shutdownCtx)
	}()

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint), otlpmetrichttp.WithInsecure())
	if err != nil {
		return fmt.Errorf("metric exporter: %w", err)
	}
	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			metric.WithInterval(interval))),
		metric.WithResource(res))
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = meterProvider.Shutdown(shutdownCtx)
	}()

	counter, err := meterProvider.Meter("e2e").Int64Counter("e2e.emitter.ticks")
	if err != nil {
		return fmt.Errorf("create counter: %w", err)
	}
	tracer := tracerProvider.Tracer("e2e")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
			_, span := tracer.Start(ctx, "e2e.tick")
			counter.Add(ctx, 1)
			// stdout is what the file_log receiver collects, so the logs
			// pipeline is exercised without an OTLP log exporter.
			fmt.Printf(`{"level":"info","msg":"e2e tick","service":%q}`+"\n", service)
			span.End()
		}
	}
}
