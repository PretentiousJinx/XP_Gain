package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
)

func domainMacros(kcal, protein, carbs, fat int) domain.Macros {
	return domain.Macros{KCal: kcal, ProteinG: protein, CarbsG: carbs, FatG: fat}
}

func domainGoals(kcal, protein, carbs, fat int) domain.Goals {
	return domain.Goals{KCal: kcal, ProteinG: protein, CarbsG: carbs, FatG: fat}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds())
	})
}

// withRecovery converts a panic into a 500 instead of tearing down the process.
// It pairs with the rollback in store.WithTx: that releases the sole writer
// connection, and this keeps the server alive to use it.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("panic recovered", "panic", p, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, ErrorBody{
					Code: "internal_error", Message: "Something went wrong. Please try again.",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
