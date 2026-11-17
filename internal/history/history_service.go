package history

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
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
}

// NewHistoryService builds the service from the investigation pool (which may
// be nil), the logger, the Watchdog build version, and optional initial servers.
func NewHistoryService(pool *pgxpool.Pool, logger *slog.Logger, version string, servers ...[]models.ObaServer) *HistoryService {
	var initial []models.ObaServer
	if len(servers) > 0 {
		initial = servers[0]
	}
	return &HistoryService{
		pool:     pool,
		logger:   logger,
		version:  version,
		resolver: NewServerNameResolver(initial),
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
