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
	"strings"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/announce"
	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/health"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/reply"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/version"
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
	// Like -db: for a binary run outside a container, where 8080 may already
	// belong to something else on the machine. A deployment leaves it alone
	// and maps the port from outside.
	port := fs.Int("port", 0, "port to listen on (default "+config.ListenAddr+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr, err := config.ListenAddrFor(*port)
	if err != nil {
		return err
	}
	if *healthcheck {
		// The probe has to knock on the port this invocation was told to use,
		// not the default, or a server on another one looks dead.
		health.Run("http://127.0.0.1" + addr + "/healthz")
	}

	// Read before the logger exists: an unrecognised LOG_LEVEL is a startup
	// error like any other bad variable, and the level it names is what the
	// logger itself must be built with.
	cfg, err := config.Load(dbPath)
	if err != nil {
		return err
	}
	bridgeCfg, err := config.LoadBridge()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	// Stamped first, because a log scrolled back to days ago cannot say
	// which binary produced it otherwise — precisely the confusion
	// internal/version exists to end. "wordleland version" answers "which
	// build" on demand; this stamps the log stream with it instead.
	logger.Info("wordleland starting",
		"version", version.String(),
		"log_level", cfg.LogLevel.String(),
		"bridge_enabled", bridgeCfg != nil,
	)

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// The server is the only process that migrates: a single migrator means
	// two cannot race on first start, and the CLI runs against a file this
	// has already prepared.
	//
	// Logged only when there is something to do: on every ordinary start
	// this is empty and silent, the same as it always was. When it isn't,
	// a migration slow enough to matter (a backfill over a large table,
	// say) would otherwise look like the process hanging between "database
	// ready" and the next line, with nothing in the log to tell an operator
	// what it's waiting on.
	pending, err := store.PendingMigrations(ctx, db, store.Migrations())
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		logger.Info("running database migrations", "count", len(pending), "migrations", pending)
	}
	start := time.Now()
	if err := store.Migrate(ctx, db, store.Migrations()); err != nil {
		return err
	}
	if len(pending) > 0 {
		logger.Info("database migrations applied", "count", len(pending), "elapsed", time.Since(start))
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

	// Configuration, not a verb flag, precisely so this line exists: a
	// production instance with it on by accident must say so at boot rather
	// than silently arming a verb that deletes players.
	if cfg.DemoMode {
		logger.Warn("DEMO_MODE is on; the `demo` CLI verb can generate and delete players. " +
			"This must not be set on a production instance.")
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
	var monthly, daily, weekly func(context.Context, time.Time) error
	var announcer bridge.Announcer
	var respond bridge.Responder
	var model *reply.Ollama
	if bridgeCfg != nil {
		// Delivery is a direct call now. The bridge writes as the
		// application itself rather than as a token holder, because since
		// the services merged it is not an API client — it is us.
		deliver := func(ctx context.Context, sub ingest.Submission) (ingest.Result, error) {
			return ingest.Apply(ctx, db, store.SystemActor(), sub, true)
		}

		// Nil when announcing is off: the bridge treats a nil Announcer as
		// "never call this", so turning the feature off costs nothing at
		// every message instead of a check here plus a check there. The
		// Responder is nil the same way when replies are off.
		if bridgeCfg.AnnounceMonths || bridgeCfg.AnnounceDays || bridgeCfg.AnnounceWeeks || bridgeCfg.Replies {
			cats, err := i18n.Load()
			if err != nil {
				return err
			}
			send, err := bridge.NewSender(bridgeCfg.SignalAPIURL, bridgeCfg.SignalAccount, bridgeCfg.SignalGroupID)
			if err != nil {
				return err
			}
			if bridgeCfg.Replies {
				model = reply.NewOllama(bridgeCfg.LLMURL, bridgeCfg.LLMModel)
				answer := reply.New(db, cats, bridgeCfg.AnnounceLocale, model, send, logger)
				respond = func(ctx context.Context, m bridge.Message) error {
					// The mention itself is a placeholder character in the
					// text; the question is what is left. A reply to one of
					// the bot's own posts brings that post along.
					return answer(ctx, m.SenderUUID, strings.ReplaceAll(m.Body, bridge.MentionPlaceholder, ""), m.Quoted)
				}
			}
			if bridgeCfg.AnnounceDays {
				// Before anything can check: on a deployment that has never
				// announced a day, this marks yesterday done so the first
				// thing the bot says is about the puzzle being played now,
				// not a recap of a day the group has moved on from.
				if err := announce.SkipDailyBacklog(ctx, db, time.Now()); err != nil {
					return err
				}
				daily = announce.NewDaily(db, cats, bridgeCfg.AnnounceLocale,
					bridgeCfg.AnnounceMonths, send)
			}
			if bridgeCfg.AnnounceWeeks {
				// The same first-run rule as the day's, for the week.
				if err := announce.SkipWeeklyBacklog(ctx, db, time.Now()); err != nil {
					return err
				}
				weekly = announce.NewWeekly(db, cats, bridgeCfg.AnnounceLocale,
					bridgeCfg.AnnounceDays, send)
			}
			if bridgeCfg.AnnounceMonths {
				monthly = announce.NewMonthly(db, cats, bridgeCfg.AnnounceLocale,
					bridgeCfg.AnnounceDays, bridgeCfg.AnnounceWeeks, send)
			}
			// One Announcer, every check. Each reports "nothing to do" as a
			// nil error, so running them all after every message is how any
			// of them catches up a scheduled run the app was down for.
			// Joined rather than short-circuited: a failing check must not
			// cost the others their post. Smallest first, which is what lets
			// the last result of a month ending on a Sunday post the day, the
			// week and the month, in that order.
			announcer = joinChecks(daily, weekly, monthly)
		}

		b, err := bridge.New(*bridgeCfg, deliver, announcer, respond, logger)
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
	srv.SetBridgeConfig(bridgeCfg)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// The live streams never go idle on their own, and Shutdown below waits
	// for idle. This ends them the moment shutdown starts.
	httpSrv.RegisterOnShutdown(srv.Close)

	var wg sync.WaitGroup
	if supervisor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervisor.Run(ctx)
		}()
		logger.Info("signal bridge started")

		if model != nil {
			// Alongside, not before: the model server starts after the app
			// often enough, and the first pull takes minutes. A question
			// that arrives before it is done is told to ask again.
			wg.Add(1)
			go func() {
				defer wg.Done()
				model.Prepare(ctx, logger)
			}()
			logger.Info("replies started", "model", bridgeCfg.LLMModel,
				"on", "a message that mentions the bot")
		}

		// One run just after midnight for all three, in the Announcer's
		// order: the day, the week and the month all close at a midnight.
		if announcer != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				announce.RunMidnight(ctx, announcer, logger)
			}()
			logger.Info("announcement scheduler started", "at", "00:01",
				"day", daily != nil, "week", weekly != nil, "month", monthly != nil,
				"early", "posted as soon as every active player has filed",
				"on_start", "checks once now, to catch up a midnight missed while down")
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
		logger.Info("listening", "addr", addr)
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

// joinChecks runs every non-nil check in order and reports every failure,
// so one announcement going wrong does not stop the next from running.
func joinChecks(checks ...func(context.Context, time.Time) error) func(context.Context, time.Time) error {
	return func(ctx context.Context, now time.Time) error {
		var errs []error
		for _, check := range checks {
			if check != nil {
				errs = append(errs, check(ctx, now))
			}
		}
		return errors.Join(errs...)
	}
}
