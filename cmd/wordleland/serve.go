package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/announce"
	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/health"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/web"
)

// shutdownTimeout bounds how long in-flight requests get to finish after a
// SIGTERM before the process exits anyway.
const shutdownTimeout = 10 * time.Second

// janitorInterval sets how often expired state is purged. The deletes are
// cheap at this scale, so there is no need for finer granularity than the
// rate limiter's own window.
const janitorInterval = auth.DefaultWindow

// runJanitor periodically purges state that nothing else reaps on its own:
// expired rate-limit buckets, expired sessions, spent or expired password
// reset tokens, and — when PENDING_RETENTION is set — held results past
// their retention window.
func runJanitor(ctx context.Context, db *sql.DB, limiter *auth.Limiter, retention time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			limiter.Cleanup()
			if n, err := store.DeleteExpiredSessions(ctx, db); err != nil {
				logger.Error("purge expired sessions", "error", err)
			} else if n > 0 {
				logger.Info("purged expired sessions", "count", n)
			}
			if n, err := store.DeleteExpiredResetTokens(ctx, db); err != nil {
				logger.Error("purge expired reset tokens", "error", err)
			} else if n > 0 {
				logger.Info("purged expired reset tokens", "count", n)
			}
			if n, err := store.DeleteExpiredPendingResults(ctx, db, retention); err != nil {
				logger.Error("purge expired pending results", "error", err)
			} else if n > 0 {
				logger.Info("purged expired pending results", "count", n)
			}
		}
	}
}

// runServe starts the server, and the Signal bridge if one is configured.
//
// This is the one subcommand that migrates and the one that runs forever;
// every other verb expects a database the server has already prepared.
func runServe(ctx context.Context, args []string, dbPath string, out io.Writer) error {
	fs := flag.NewFlagSet("wordleland serve", flag.ContinueOnError)
	fs.SetOutput(out)
	// The runtime image has no shell, so the container healthcheck runs the
	// binary itself rather than curl.
	healthcheck := fs.Bool("healthcheck", false,
		"probe the local health endpoint and exit; used by the container healthcheck")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *healthcheck {
		health.Run("http://127.0.0.1" + config.ListenAddr + "/healthz")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(dbPath)
	if err != nil {
		return err
	}
	bridgeCfg, err := config.LoadBridge()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// The server is the only process that migrates: a single migrator means
	// two cannot race on first start, and the CLI runs against a file this
	// has already prepared.
	if err := store.Migrate(ctx, db, store.Migrations()); err != nil {
		return err
	}
	logger.Info("database ready", "path", cfg.DBPath)

	// Empty is a working configuration, which is exactly why it is worth a
	// line: nothing else about a degraded rate limiter is visible. Behind a
	// proxy every request arrives from the same address, so the per-address
	// budget becomes one budget for everybody and ten failed logins from
	// anyone locks out everyone. Running with no proxy at all is the case
	// where this is correct, and there the warning is one line at boot.
	if len(cfg.TrustedProxies) == 0 {
		logger.Warn("TRUSTED_PROXIES is empty; if anything proxies this, " +
			"every client shares one rate-limit budget and any of them can " +
			"lock out the rest. Set it to the proxy's address range.")
	}

	if err := bootstrapAdmin(ctx, db, cfg, logger); err != nil {
		return err
	}

	// Also the only process that mints the share slug, for the same reason.
	slug, created, err := store.EnsureShareSlug(ctx, db)
	if err != nil {
		return err
	}
	if created {
		logger.Info("generated share link", "path", "/share/"+slug+"/")
	}

	var supervisor *bridge.Supervisor
	var announcer bridge.Announcer
	if bridgeCfg != nil {
		// Delivery is a direct call now. The bridge writes as the
		// application itself rather than as a token holder, because since
		// the services merged it is not an API client — it is us.
		deliver := func(ctx context.Context, sub ingest.Submission) (ingest.Status, error) {
			return ingest.Apply(ctx, db, store.SystemActor(), sub, true)
		}

		// Nil when announcing is off: the bridge treats a nil Announcer as
		// "never call this", so turning the feature off costs nothing at
		// every message instead of a check here plus a check there.
		if bridgeCfg.AnnounceMonths {
			cats, err := i18n.Load()
			if err != nil {
				return err
			}
			send, err := bridge.NewSender(bridgeCfg.SignalAPIURL, bridgeCfg.SignalAccount, bridgeCfg.SignalGroupID)
			if err != nil {
				return err
			}
			announcer = announce.New(db, cats, bridgeCfg.AnnounceLocale, send)
		}

		b, err := bridge.New(*bridgeCfg, deliver, announcer, logger)
		if err != nil {
			return err
		}
		supervisor = bridge.Supervise(b, logger)
	}

	srv, err := web.New(cfg, db, logger)
	if err != nil {
		return err
	}
	srv.SetBridge(supervisor)

	httpSrv := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var wg sync.WaitGroup
	if supervisor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervisor.Run(ctx)
		}()
		logger.Info("signal bridge started")

		if announcer != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				announce.RunMonthly(ctx, announcer, logger)
			}()
			logger.Info("monthly announcement scheduler started", "at", "12:00 on day 1")
		}
	} else {
		// Said plainly, because an app that silently is not bridging looks
		// exactly like one whose group has gone quiet.
		logger.Info("no signal bridge configured; results can still be entered by hand",
			"enable", "set SIGNAL_ACCOUNT and SIGNAL_GROUP_ID")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		runJanitor(ctx, db, srv.Limiter(), cfg.PendingRetention, logger)
	}()
	logger.Info("housekeeping janitor started", "interval", janitorInterval)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", config.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// A fresh context: ctx is already cancelled by the signal, and Shutdown
	// needs its own deadline to drain in-flight requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	wg.Wait()
	logger.Info("stopped")
	return nil
}

// bootstrapAdmin creates the first administrator, if the deployment asked
// for one and the database is empty.
//
// The one exception to "the CLI creates users": a fresh deployment would
// otherwise need a shell into the container before anyone could log in at
// all. It grants no shortcut past the security model — 2FA is still
// mandatory for admins, so the first login goes straight to enrolment.
func bootstrapAdmin(ctx context.Context, db *sql.DB, cfg *config.Config, logger *slog.Logger) error {
	if cfg.AdminEmail == "" {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("hash the bootstrap password: %w", err)
	}
	user, created, err := store.BootstrapAdmin(ctx, db, cfg.AdminEmail, hash)
	if err != nil {
		return err
	}
	if created {
		logger.Info("created the first administrator from the environment",
			"email", user.Email,
			"next", "sign in; two-factor enrolment is required before anything else")
	}
	return nil
}
