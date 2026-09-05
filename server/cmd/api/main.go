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
	"strings"
	"syscall"
	"time"

	// Embeds the IANA zone database in the binary. Without it, time.LoadLocation
	// depends on OS-provided zone files, which Windows does not ship -- every
	// user's local date would silently collapse to UTC and break their streak.
	_ "time/tzdata"

	"github.com/PretentiousJinx/xpgain/server/internal/httpapi"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

func main() {
	var (
		addr    = flag.String("addr", ":8080", "listen address")
		dbPath  = flag.String("db", "xpgain.db", "path to the SQLite database file")
		readers = flag.Int("readers", 4, "size of the read connection pool")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	db, err := store.Open(*dbPath, *readers)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	api := httpapi.New(service.New(db), bearerUserID)

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

// bearerUserID is a development stand-in for real auth. It trusts the bearer
// token as a user ID.
//
// PLACEHOLDER -- do not ship. Replace with verification of a signed token
// (Firebase Auth ID token, or your own JWT) before this service is reachable
// from anything but localhost. As written, any client can act as any user by
// typing their ID.
func bearerUserID(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}
