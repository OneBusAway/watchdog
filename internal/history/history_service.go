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
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

// HistoryService holds the dependencies every recording step needs. Its
// methods are thin wrappers over the package-private functions, which take
// their dependencies as explicit parameters so tests can call them directly.
type HistoryService struct {
	// pool is nil when investigation storage is disabled. Every method
	// returns straight away in that case.
	pool     *pgxpool.Pool
	logger   *slog.Logger
	version  string
	resolver *ServerNameResolver
	boxes    *geo.BoundingBoxStore
	static   *currentStatic
}

// NewHistoryService builds the service from the investigation pool (which may
// be nil), the logger, the Watchdog build version, configured servers for
// canonical display name resolution, and the bounding box store whose boxes a
// static version records (nil records no boxes). The version is recorded with
// history that depends on how Watchdog parsed its input.
func NewHistoryService(pool *pgxpool.Pool, logger *slog.Logger, version string, servers []models.ObaServer, boxes *geo.BoundingBoxStore) *HistoryService {
	return &HistoryService{
		pool:     pool,
		logger:   logger,
		version:  version,
		resolver: NewServerNameResolver(servers),
		boxes:    boxes,
		static:   newCurrentStatic(),
	}
}

// SetServers updates the configured servers for canonical name resolution.
func (h *HistoryService) SetServers(servers []models.ObaServer) {
	if h.resolver == nil {
		h.resolver = NewServerNameResolver(servers)
		return
	}
	h.resolver.Update(servers)
}

// Resolver returns the active ServerNameResolver.
func (h *HistoryService) Resolver() *ServerNameResolver {
	return h.resolver
}

// EnsureServer resolves the server's canonical display name and records or
// updates its row in onebusaway_server.
func (h *HistoryService) EnsureServer(ctx context.Context, q *dbgen.Queries, server models.ObaServer) (int64, error) {
	return ensureServer(ctx, q, server, h.resolver)
}
