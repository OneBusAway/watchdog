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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/gtfs"
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
	static   *currentStatic
	// stops holds each server's open unmatched-stop episodes, so a poll
	// that opens or closes nothing does not touch the database.
	stops *openStopEpisodes
	// staticStore is Watchdog's static GTFS, read for stop metadata and trip
	// schedules. It is nil until SetStaticStore is called; stops then
	// resolve as not found and trips are not classified.
	staticStore *gtfs.StaticStore
	// vehicles holds each server's open vehicle anomaly episodes, so a
	// snapshot that changes nothing does not touch the database.
	vehicles *openVehicleEpisodes
	// rules holds the vehicle anomaly rule versions in effect.
	rules *vehicleRuleVersions
	// fetchInterval is the collection interval the stale threshold is
	// derived from.
	fetchInterval time.Duration
	// startedAt is when this process started applying the vehicle anomaly
	// rules: the effective_at of any rule version it records.
	startedAt time.Time
}

// NewHistoryService builds the service from the investigation pool (which may
// be nil), the logger, the Watchdog build version, and configured servers for
// canonical display name resolution. The version is recorded with history that
// depends on how Watchdog parsed its input.
func NewHistoryService(pool *pgxpool.Pool, logger *slog.Logger, version string, servers []models.ObaServer) *HistoryService {
	return &HistoryService{
		pool:      pool,
		logger:    logger,
		version:   version,
		resolver:  NewServerNameResolver(servers),
		static:    newCurrentStatic(),
		stops:     newOpenStopEpisodes(),
		vehicles:  newOpenVehicleEpisodes(),
		rules:     &vehicleRuleVersions{},
		startedAt: time.Now().UTC(),
	}
}

// SetStaticStore sets the static GTFS store that stop metadata is read from.
// app.New calls it once, before collection starts.
func (h *HistoryService) SetStaticStore(store *gtfs.StaticStore) {
	h.staticStore = store
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
