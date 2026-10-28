package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup installs the slog default (JSON to stdout, plus OTLP logs when logsURL
// is set) and a tracer provider exporting OTLP to tracesURL. Empty URLs
// disable export. The returned func flushes exporters.
func Setup(ctx context.Context, service, tracesURL, logsURL string) (func(context.Context) error, error) {
	res := resource.NewSchemaless(attribute.String("service.name", service))
	var shutdown []func(context.Context) error
	handlers := []slog.Handler{slog.NewJSONHandler(os.Stdout, nil)}
	if tracesURL != "" {
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(tracesURL))
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		shutdown = append(shutdown, tp.Shutdown)
	}
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if logsURL != "" {
		exp, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(logsURL))
		if err != nil {
			return nil, err
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)), sdklog.WithResource(res))
		handlers = append(handlers, otelslog.NewHandler(service, otelslog.WithLoggerProvider(lp)))
		shutdown = append(shutdown, lp.Shutdown)
	}
	slog.SetDefault(slog.New(NewHandler(handlers...)))
	return func(ctx context.Context) error {
		var errs []error
		for _, f := range shutdown {
			errs = append(errs, f(ctx))
		}
		return errors.Join(errs...)
	}, nil
}
