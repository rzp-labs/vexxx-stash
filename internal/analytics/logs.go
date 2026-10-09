package analytics

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

var (
	logsProvider *sdklog.LoggerProvider
	logsLogger   otellog.Logger
)

// InitializeLogs configures an isolated logger for the small set of lifecycle
// records added by this integration. It deliberately does not attach to the
// application's logrus loggers, so existing application logs stay local.
func InitializeLogs() error {
	projectToken, host := posthogDestination()
	if projectToken == "" || host == "" {
		return nil
	}

	exporter, err := otlploghttp.New(context.Background(),
		otlploghttp.WithEndpointURL(host+"/i/v1/logs"),
		otlploghttp.WithHeaders(map[string]string{
			"Authorization": "Bearer " + projectToken,
		}),
	)
	if err != nil {
		return fmt.Errorf("create PostHog log exporter: %w", err)
	}

	logsProvider = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(observedLogExporter{exporter})),
		sdklog.WithResource(resource.NewSchemaless(attribute.String("service.name", "vexxx-server"), attribute.String("service.version", ReleaseProperties()["app_version"].(string)), attribute.String("service.build", ReleaseProperties()["app_revision"].(string)), attribute.String("telemetry.sdk.language", "go"))),
	)
	logsLogger = logsProvider.Logger("stash.posthog_logs")

	return nil
}

// LogInfo exports an integration-owned lifecycle record without affecting the
// application's existing loggers or their configured outputs.
func LogInfo(message string) {
	if logsProvider == nil {
		return
	}

	var record otellog.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(otellog.SeverityInfo)
	record.SetSeverityText("INFO")
	record.SetBody(otellog.StringValue(diagnostics.Safe(message, nil)))
	logsLogger.Emit(context.Background(), record)
}

// CloseLogs flushes only the isolated PostHog log provider during shutdown.
func CloseLogs(ctx context.Context) error {
	if logsProvider == nil {
		return nil
	}

	return logsProvider.Shutdown(ctx)
}

var logExportFailures atomic.Uint64

type observedLogExporter struct{ sdklog.Exporter }

func (e observedLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	err := e.Exporter.Export(ctx, records)
	if err != nil {
		logExportFailures.Add(uint64(len(records)))
		deliveryWarning("logs")
	}
	return err
}
