// Package history records Watchdog's observations in the optional PostgreSQL
// investigation store, so problems can be investigated after the current
// upstream snapshot has moved on.
//
// Recording follows the design in docs/investigation-api.md, Section 6: it
// runs synchronously right after a collection step, only when the database is
// configured, and never stops collection (D18, D30). Watchdog writes only when
// something changes (D31), so the identity functions in identities.go run only
// inside a write that opens or closes an episode or records a new static
// version.
package history

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HistoryService holds the dependencies every recording step needs. Its
// methods are thin wrappers over the package-private functions, which take
// their dependencies as explicit parameters so tests can call them directly.
type HistoryService struct {
	// pool is nil when investigation storage is disabled. Every method
	// returns straight away in that case.
	pool    *pgxpool.Pool
	logger  *slog.Logger
	version string
}

// NewHistoryService builds the service from the investigation pool (which may
// be nil), the logger and the Watchdog build version. The version is recorded
// with history that depends on how Watchdog parsed its input.
func NewHistoryService(pool *pgxpool.Pool, logger *slog.Logger, version string) *HistoryService {
	return &HistoryService{pool: pool, logger: logger, version: version}
}
