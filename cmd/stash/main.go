//go:generate go run github.com/99designs/gqlgen
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/posthog/posthog-go"

	"github.com/spf13/pflag"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/internal/api"
	"github.com/stashapp/stash/internal/build"
	"github.com/stashapp/stash/internal/desktop"
	"github.com/stashapp/stash/internal/log"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/ui"

	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var exitCode = 0
var restartPending = false

func main() {
	defer func() {
		if restartPending {
			if err := restart(); err != nil {
				logger.Errorf("Failed to restart process: %v", err)
				os.Exit(1)
			}
			// On Unix, restart() calls syscall.Exec which replaces the process;
			// on Windows it launches a child and calls os.Exit — we never reach here.
			return
		}
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()

	defer recoverPanic()

	initLogTemp()

	helpFlag := false
	pflag.BoolVarP(&helpFlag, "help", "h", false, "show this help text and exit")

	versionFlag := false
	pflag.BoolVarP(&versionFlag, "version", "v", false, "show version number and exit")

	cpuProfilePath := ""
	pflag.StringVar(&cpuProfilePath, "cpuprofile", "", "write cpu profile to file")

	pflag.Parse()

	if helpFlag {
		pflag.Usage()
		return
	}

	if versionFlag {
		fmt.Println(build.VersionString())
		return
	}

	cfg, err := config.Initialize()
	if err != nil {
		exitError(fmt.Errorf("config initialization error: %w", err))
		return
	}

	l := initLog(cfg)

	if err := analytics.Initialize(); err != nil {
		exitError(fmt.Errorf("PostHog initialization error: %w", err))
		return
	}
	defer func() {
		if err := analytics.Close(); err != nil {
			logger.Errorf("error closing PostHog client: %v", err)
		}
	}()
	defer recoverPanic()

	if err := analytics.InitializeLogs(); err != nil {
		exitError(fmt.Errorf("PostHog log initialization error: %w", err))
		return
	}
	defer func() {
		analytics.LogInfo("stash_server_stopping")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := analytics.CloseLogs(ctx); err != nil {
			logger.Errorf("error closing PostHog log exporter: %v", err)
		}
	}()

	if cpuProfilePath != "" {
		if err := initProfiling(cpuProfilePath); err != nil {
			exitError(err)
			return
		}
		defer pprof.StopCPUProfile()
	}

	// initialise desktop.IsDesktop here so that it doesn't get affected by
	// ffmpeg hardware checks later on
	desktop.InitIsDesktop()

	mgr, err := manager.Initialize(cfg, l)
	if err != nil {
		exitError(fmt.Errorf("manager initialization error: %w", err))
		return
	}
	defer mgr.Shutdown()

	server, err := api.Initialize()
	if err != nil {
		exitError(fmt.Errorf("api initialization error: %w", err))
		return
	}
	defer server.Shutdown()

	exit := make(chan int)
	mgr.SetExitChannel(exit)
	analytics.LogInfo("stash_server_starting")

	go func() {
		err := server.Start()
		if !errors.Is(err, http.ErrServerClosed) {
			exitError(fmt.Errorf("http server error: %w", err))
			exit <- 1
		}
	}()

	go handleSignals(exit)
	desktop.Start(exit, &ui.FaviconProvider)

	code := <-exit
	if code == manager.ExitCodeRestart {
		restartPending = true
		exitCode = 0
	} else {
		exitCode = code
	}
}

// initLogTemp initializes a temporary logger for use before the config is loaded.
// Logs only error level message to stderr.
func initLogTemp() *log.Logger {
	l := log.NewLogger()
	l.Init("", true, "Error", 0)
	logger.Logger = l

	return l
}

func initLog(cfg *config.Config) *log.Logger {
	l := log.NewLogger()
	l.Init(cfg.GetLogFile(), cfg.GetLogOut(), cfg.GetLogLevel(), cfg.GetLogFileMaxSize())
	logger.Logger = l

	return l
}

func initProfiling(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to create CPU profile file: %v", err)
	}

	if err = pprof.StartCPUProfile(f); err != nil {
		return fmt.Errorf("could not start CPU profiling: %v", err)
	}

	logger.Infof("profiling to %s", path)

	return nil
}

func recoverPanic() {
	if err := recover(); err != nil {
		exitCode = 1
		logger.Errorf("panic: %v\n%s", err, debug.Stack())

		if client := analytics.Client(); client != nil {
			exception := posthog.NewDefaultException(time.Now(), "server", "ApplicationPanic", "application panic (message redacted)")
			version, revision, _ := build.Version()
			exception.Properties = posthog.NewProperties().Set("app_version", version).Set("app_revision", revision).Set("$process_person_profile", false)
			client.Enqueue(exception)
		}

		if desktop.IsDesktop() {
			desktop.FatalError(fmt.Errorf("Panic: %v", err))
		}
	}
}

func exitError(err error) {
	exitCode = 1
	logger.Error(err)
	// #5784 - log to stdout as well as the logger
	// this does mean that it will log twice if the logger is set to stdout
	fmt.Println(err)
	if desktop.IsDesktop() {
		desktop.FatalError(err)
	}
}

func handleSignals(exit chan<- int) {
	// handle signals
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	<-signals
	exit <- 0
}
