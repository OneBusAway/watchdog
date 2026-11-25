// Package investigation serves the read-only Investigation API described in
// docs/investigation-api.md. Every route goes through the same request
// pipeline (see InvestigationService.get), so endpoints only supply a
// query, a handler and one s.get line.
package investigation

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"watchdog.onebusaway.org/internal/models"
)

// InvestigationService holds the dependencies the investigation handlers
// share.
type InvestigationService struct {
	// Pool is the investigation database. It is nil when investigation
	// storage is disabled or was unavailable at startup.
	Pool *pgxpool.Pool
	// Servers returns the current configuration (cfg.GetServers), read on
	// every call so a --config-url refresh is visible.
	Servers func() []models.ObaServer
	Logger  *slog.Logger

	// tokens are the accepted bearer tokens. They are never logged.
	tokens [][]byte
	// now is the clock behind time-window defaults and "not in the future"
	// checks. Tests replace it to fix "now".
	now func() time.Time
	// loaders hold one loader per includable resource kind; see include.go.
	loaders map[resourceKind]loader

	// fetchInterval is the duration between collection ticks, used as the
	// max-age for short-cached responses. It defaults to changingMaxAge.
	fetchInterval atomic.Int64 // nanoseconds
}

// NewInvestigationService builds the service. pool may be nil. tokens are the
// accepted bearer tokens; empty values are ignored.
func NewInvestigationService(pool *pgxpool.Pool, servers func() []models.ObaServer, tokens []string, logger *slog.Logger) *InvestigationService {
	accepted := make([][]byte, 0, len(tokens))
	for _, token := range tokens {
		if token != "" {
			accepted = append(accepted, []byte(token))
		}
	}
	s := &InvestigationService{
		Pool:    pool,
		Servers: servers,
		Logger:  logger,
		tokens:  accepted,
		now:     time.Now,
		loaders: map[resourceKind]loader{},
	}
	s.fetchInterval.Store(int64(changingMaxAge))
	return s
}

// SetFetchInterval sets the collection interval used for short-cached responses.
// When non-positive, changingMaxAge (30 seconds) is used.
func (s *InvestigationService) SetFetchInterval(interval time.Duration) {
	if interval <= 0 {
		interval = changingMaxAge
	}
	s.fetchInterval.Store(int64(interval))
}

// FetchInterval returns the collection interval used for short-cached responses.
func (s *InvestigationService) FetchInterval() time.Duration {
	return s.shortCacheDuration()
}
