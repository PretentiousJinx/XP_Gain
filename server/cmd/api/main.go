// Command api runs the XP_Gain macro intake service.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embeds the IANA zone database in the binary. Without it, time.LoadLocation
	// depends on OS-provided zone files, which Windows does not ship -- every
	// user's local date would silently collapse to UTC and break their streak.
	_ "time/tzdata"

	"github.com/PretentiousJinx/xpgain/server/internal/auth"
	"github.com/PretentiousJinx/xpgain/server/internal/firebase"
	"github.com/PretentiousJinx/xpgain/server/internal/httpapi"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

func main() {
	var (
		addr    = flag.String("addr", ":8080", "listen address")
		dbPath  = flag.String("db", "xpgain.db", "path to the SQLite database file")
		readers = flag.Int("readers", 4, "size of the read connection pool")
		project = flag.String("firebase-project", os.Getenv("FIREBASE_PROJECT_ID"),
			"Firebase project ID (or set FIREBASE_PROJECT_ID)")
		credsPath = flag.String("google-credentials", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
			"path to a service-account JSON key; enables Firestore sync and revocation checks")
		syncEvery = flag.Duration("sync-interval", 30*time.Second, "how often to sweep dirty rows to Firestore")
		revokeTTL = flag.Duration("revocation-ttl", time.Minute,
			"how long an account's revocation state is cached; this is the revocation latency")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Refuse to start without a project ID rather than falling back to an
	// unauthenticated mode. A server that boots and quietly accepts anonymous
	// traffic is far more dangerous than one that fails loudly here: the
	// project ID is what binds a token to *this* Firebase project, and without
	// it a valid ID token from any other project would authenticate.
	if *project == "" {
		slog.Error("firebase auth is not configured",
			"hint", "pass -firebase-project or set FIREBASE_PROJECT_ID")
		os.Exit(1)
	}

	authOpts := []auth.Option{}
	var pusher store.Pusher

	// Service-account credentials are optional, but their absence disables two
	// distinct things, so say so plainly rather than degrading in silence.
	if *credsPath == "" {
		slog.Warn("no service-account credentials: Firestore sync is DISABLED and "+
			"revoked sessions will remain valid until their tokens expire",
			"hint", "pass -google-credentials or set GOOGLE_APPLICATION_CREDENTIALS")
	} else {
		sa, err := firebase.LoadServiceAccount(*credsPath)
		if err != nil {
			slog.Error("load service account", "err", err, "path", *credsPath)
			os.Exit(1)
		}
		if sa.ProjectID != *project {
			// Pushing one project's data into another's Firestore, or checking
			// revocation against the wrong directory, both fail confusingly and
			// late. Catch the mismatch at boot.
			slog.Error("service account belongs to a different project",
				"credential_project", sa.ProjectID, "configured_project", *project)
			os.Exit(1)
		}

		tokens := firebase.NewTokenSource(sa)

		fs := firebase.NewFirestore(*project, tokens)
		pusher = fs

		identity := firebase.NewIdentity(*project, tokens)
		identity.TTL = *revokeTTL
		authOpts = append(authOpts, auth.WithRevocationChecker(identity))

		slog.Info("firebase configured",
			"project", *project,
			"service_account", sa.ClientEmail,
			"revocation_ttl", revokeTTL.String())
	}

	verifier, err := auth.NewVerifier(*project, authOpts...)
	if err != nil {
		slog.Error("build token verifier", "err", err)
		os.Exit(1)
	}

	db, err := store.Open(*dbPath, *readers)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancelSweeper := context.WithCancel(context.Background())
	defer cancelSweeper()

	var sweeper *store.Sweeper
	if pusher != nil {
		sweeper = &store.Sweeper{DB: db, Push: pusher, Interval: *syncEvery}
		go sweeper.Run(ctx)
		slog.Info("sync sweeper started", "interval", syncEvery.String())
	}

	api := httpapi.New(service.New(db), verifier)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", *addr, "db", *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Drain in-flight requests before closing the DB, so no transaction is cut
	// off mid-commit.
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}

	// Stop the periodic sweeper, then flush once with requests already drained,
	// so the last few seconds of work are not left dirty until the next boot.
	cancelSweeper()
	if sweeper != nil {
		flushCtx, cancelFlush := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelFlush()
		sweeper.FlushOnShutdown(flushCtx)
	}
}
