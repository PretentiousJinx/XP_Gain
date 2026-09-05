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
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Refuse to start without a project ID rather than falling back to an
	// unauthenticated mode. A server that boots and quietly accepts anonymous
	// traffic is far more dangerous than one that fails loudly here: the
	// project ID is what binds a token to *this* Firebase project, and without
	// it a valid ID token from any other project would authenticate.
	verifier, err := auth.NewVerifier(*project)
	if err != nil {
		slog.Error("firebase auth is not configured",
			"err", err,
			"hint", "pass -firebase-project or set FIREBASE_PROJECT_ID")
		os.Exit(1)
	}
	slog.Info("firebase auth configured", "project", *project)

	db, err := store.Open(*dbPath, *readers)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
