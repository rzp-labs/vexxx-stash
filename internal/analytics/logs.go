package analytics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
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
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
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
	record.SetBody(otellog.StringValue(message))
	logsLogger.Emit(context.Background(), record)
}

// CloseLogs flushes only the isolated PostHog log provider during shutdown.
func CloseLogs(ctx context.Context) error {
	if logsProvider == nil {
		return nil
	}

	return logsProvider.Shutdown(ctx)
}
