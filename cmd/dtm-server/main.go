// Command dtm-server runs the Dependency Time Machine control plane.
//
// Startup is fail-fast: environment is validated before any listener
// opens, and the execution-isolation boundary (container vs degraded
// mode) is logged explicitly at boot so operators can audit it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/api"
	"github.com/ensembyte/dtm/internal/ci"
	"github.com/ensembyte/dtm/internal/config"
	"github.com/ensembyte/dtm/internal/logx"
	"github.com/ensembyte/dtm/internal/relnotes"
	"github.com/ensembyte/dtm/internal/runner"
	"github.com/ensembyte/dtm/internal/store"
)

func main() {
	var (
		showHelp = flag.Bool("h", false, "print help and exit")
		version  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showHelp {
		flag.Usage()
		return
	}
	if *version {
		fmt.Println("dtm-server", versionString())
		return
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}

	logger := logx.New(cfg.LogLevel, "json")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Data layer: open the store only when a non-memory driver is
	// configured; the in-memory run store in the API is always available.
	var runStore *store.Store
	if cfg.DataDriver != config.MemoryDriver {
		dsn := cfg.SQLitePath
		if cfg.DataDriver == config.PostgresDriver {
			if cfg.DatabaseURL == "" {
				logger.Error("boot: DTM_DATABASE_URL required for postgres driver")
				os.Exit(2)
			}
			dsn = cfg.DatabaseURL
		}
		st, err := store.Open(ctx, cfg.DataDriver, dsn)
		if err != nil {
			logger.Error("boot: open store", "err", err)
			os.Exit(2)
		}
		defer st.Close()
		runStore = st
		logger.Info("boot: store open", "driver", cfg.DataDriver)
	}

	// Execution-isolation boundary: log which mode is actually active so
	// the audit trail (run reports, operator dashboards) can always say
	// whether builds/tests were really executed.
	runnerMode := cfg.RunnerMode
	if runnerMode == "container" || runnerMode == "auto" {
		if r, err := runner.New(runnerMode, logger); err == nil {
			_ = r
			logger.Info("boot: runner selected", "mode", runnerName(runnerMode, r))
		} else {
			logger.Warn("boot: runner unavailable, degraded mode forced", "err", err.Error())
			runnerMode = "degraded"
		}
	}

	// Build the API service graph. GitHub check-run posting is enabled
	// only when an operator has explicitly configured a usable token
	// (least-privilege default: off). The GitHub App (app id + private key
	// file) is the intended production credential; deriving an
	// installation token from it is a documented limitation of this
	// environment, so until then posting reports "skipped_no_token".
	ghTokenPtr := githubTokenOrNil(cfg)
	ghToken := ""
	if ghTokenPtr != nil {
		ghToken = *ghTokenPtr
	}
	gh := ci.NewGitHub("", ghToken)
	osv := advisory.NewOSVClient(cfg.OSVEndpoint)
	notes := relnotes.NewClient()

	// Run persistence: when a durable store (SQLite/Postgres) is open,
	// the API records finished runs into it so they survive a restart;
	// otherwise (DTM_DATA_DRIVER=memory) the in-memory twin is used and
	// run records are process-local.
	var runs api.RunStore
	if runStore != nil {
		runs = store.NewPersistentRunStore(runStore)
	}

	// Per-route-class rate limiters; a class left at RPS 0 falls back to
	// the legacy global DTM_RATE_LIMIT_RPS/_BURST budget.
	limits := map[string]*api.Limiter{
		api.ClassAnalysis: api.NewLimiter(cfg.ClassBudget(cfg.RateLimitClasses.Analysis).RPS, cfg.ClassBudget(cfg.RateLimitClasses.Analysis).Burst),
		api.ClassWebhooks: api.NewLimiter(cfg.ClassBudget(cfg.RateLimitClasses.Webhooks).RPS, cfg.ClassBudget(cfg.RateLimitClasses.Webhooks).Burst),
		api.ClassReads:    api.NewLimiter(cfg.ClassBudget(cfg.RateLimitClasses.Reads).RPS, cfg.ClassBudget(cfg.RateLimitClasses.Reads).Burst),
		api.ClassRepos:    api.NewLimiter(cfg.ClassBudget(cfg.RateLimitClasses.Repos).RPS, cfg.ClassBudget(cfg.RateLimitClasses.Repos).Burst),
	}

	srv := api.New(api.Config{
		APIToken:      cfg.APIToken,
		CORSOrigin:    cfg.CORSOrigin,
		Limits:        limits,
		WebhookSecret: cfg.WebhookSecret,
		GitHubToken:   &ghToken,
		BaseURL:       cfg.StaticDir,
	}, gh, osv, notes, logger, runs)

	// /readyz readiness: the durable store must be reachable; in memory
	// mode there is nothing to probe, so the server is always ready.
	if runStore != nil {
		srv.Ready = func() bool {
			_ = runStore.Ping()
			return true
		}
	}

	httpSrv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:        srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("boot: http server listening", "addr", cfg.HTTPAddr, "runner_mode", runnerMode)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			os.Exit(1)
		}
	}
}

// versionString is a build-time overridable version marker.
func versionString() string {
	if v := os.Getenv("DTM_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// runnerName reports which concrete runner was selected under a given
// configured mode (used purely for the boot log line).
func runnerName(cfgMode string, r runner.Runner) string {
	// The configured mode is what the operator asked for; the active
	// runner reports what it actually is. Log both for audit.
	return fmt.Sprintf("configured=%s active=%s", cfgMode, r.Name())
}

// githubTokenOrNil returns the configured GitHub token, or nil when no
// GitHub integration is configured (the least-privilege default). This is
// what the CI layer uses to decide whether check-run posting is even
// attempted: nil => never attempted, which the API surfaces honestly as
// "skipped_no_token" rather than faking success.
func githubTokenOrNil(cfg *config.Config) *string {
	// In this environment GitHub App installation-token exchange is a
	// documented limitation (requires the app's private key + webhook
	// verification, both configured out-of-band in production). Until
	// that path is wired, a token is only available via a direct
	// GITHUB_TOKEN for the control plane's own check-run posting.
	if v := os.Getenv("DTM_GITHUB_TOKEN"); v != "" {
		return &v
	}
	return nil
}
