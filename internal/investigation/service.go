// Package investigation serves the read-only Investigation API described in
// docs/investigation-api.md. Every route goes through the same request
// pipeline (see InvestigationService.get), so endpoints only supply a
// query, a handler and one s.get line.
package investigation

import (
	"log/slog"
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
	return &InvestigationService{
		Pool:    pool,
		Servers: servers,
		Logger:  logger,
		tokens:  accepted,
		now:     time.Now,
	}
}
