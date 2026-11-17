package history

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// ServerNameResolver deterministically resolves the canonical display name for
// a base URL from the active configuration. When multiple agency-mode entries
// share one base URL, the first configured entry wins, preventing display_name
// from flipping on alternating writes while still allowing genuine renames to propagate.
type ServerNameResolver struct {
	mu    sync.RWMutex
	names map[string]string // normalizedBaseURL -> canonicalServerName
}

// NewServerNameResolver constructs a resolver from a slice of configured servers.
func NewServerNameResolver(servers []models.ObaServer) *ServerNameResolver {
	r := &ServerNameResolver{
		names: make(map[string]string),
	}
	r.Update(servers)
	return r
}

// Update re-indexes the canonical names from the current configuration.
func (r *ServerNameResolver) Update(servers []models.ObaServer) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.names = make(map[string]string, len(servers))
	for _, s := range servers {
		base := utils.SanitizeServerURL(s.ObaBaseURL)
		if base == "" {
			continue
		}
		if _, exists := r.names[base]; !exists {
			r.names[base] = s.ServerName
		}
	}
}

// Resolve returns the canonical display name for a base URL, falling back to
// fallbackName if the base URL is not in the configuration.
func (r *ServerNameResolver) Resolve(baseURL, fallbackName string) string {
	if r == nil {
		return fallbackName
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name, ok := r.names[baseURL]; ok && name != "" {
		return name
	}
	return fallbackName
}

// The ensure functions return the ID of an identity row, creating it when it
// does not exist yet. They run inside write, so their errors become write
// failures. Per D31 they are called only by a write that opens or closes an
// episode or records a new static version, never on an ordinary poll.
//
// URLs are normalized with utils.SanitizeServerURL, the same function the
// Prometheus labels and the investigation API's base_url filter use. A changed
// URL is a new identity with a new ID; identities are never merged (D11).
//
// An error never includes a URL, because a configured URL may carry
// credentials. It names the server instead.

// errEmptyID is returned when an external ID is empty: an empty string is
// not an identity, and storing it would merge unrelated observations.
var errEmptyID = errors.New("empty external ID")

// retryOnNoRows retries a query once if pgx.ErrNoRows is returned. Under READ COMMITTED,
// when two transactions insert the same new identity simultaneously, the losing transaction's
// statement snapshot may not see the winner's newly committed row, returning ErrNoRows.
// Retrying with a new statement within the transaction allows the new statement snapshot
// to see the winner's committed row.
func retryOnNoRows[T any](fn func() (T, error)) (T, error) {
	val, err := fn()
	if errors.Is(err, pgx.ErrNoRows) {
		return fn()
	}
	return val, err
}

// ensureServer returns the ID of the server's row. display_name follows the
// configured server name, using the resolver if provided to resolve canonical names
// when multiple entries share a base URL.
func ensureServer(ctx context.Context, q *dbgen.Queries, server models.ObaServer, resolver ...*ServerNameResolver) (int64, error) {
	baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if baseURL == "" {
		return 0, fmt.Errorf("server %q: base URL cannot be normalized", server.ServerName)
	}

	displayName := server.ServerName
	if len(resolver) > 0 && resolver[0] != nil {
		displayName = resolver[0].Resolve(baseURL, server.ServerName)
	}

	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureOneBusAwayServer(ctx, dbgen.EnsureOneBusAwayServerParams{
			NormalizedBaseUrl: baseURL,
			DisplayName:       displayName,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("server %q: ensure server: %w", server.ServerName, err)
	}
	return id, nil
}

// ensureAgency returns the ID of an agency on a server. A non-empty name
// updates current_name; an empty name keeps the stored one.
func ensureAgency(ctx context.Context, q *dbgen.Queries, serverID int64, serverName, externalAgencyID, name string) (int64, error) {
	if externalAgencyID == "" {
		return 0, fmt.Errorf("server %q: ensure agency: %w", serverName, errEmptyID)
	}
	var currentName *string
	if name != "" {
		currentName = &name
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureAgency(ctx, dbgen.EnsureAgencyParams{
			ServerID:         serverID,
			ExternalAgencyID: externalAgencyID,
			CurrentName:      currentName,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("server %q: ensure agency %q: %w", serverName, externalAgencyID, err)
	}
	return id, nil
}

// ensureEndpoint returns the ID of a vehicle-position feed URL on a server.
func ensureEndpoint(ctx context.Context, q *dbgen.Queries, serverID int64, serverName, vehiclePositionURL string) (int64, error) {
	feedURL := utils.SanitizeServerURL(vehiclePositionURL)
	if feedURL == "" {
		return 0, fmt.Errorf("server %q: vehicle-position URL cannot be normalized", serverName)
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureVehiclePositionEndpoint(ctx, dbgen.EnsureVehiclePositionEndpointParams{
			ServerID:                     serverID,
			NormalizedVehiclePositionUrl: feedURL,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("server %q: ensure vehicle-position endpoint: %w", serverName, err)
	}
	return id, nil
}

// ensureStaticSource returns the ID of a static GTFS source URL on a server.
func ensureStaticSource(ctx context.Context, q *dbgen.Queries, serverID int64, serverName, sourceURL string) (int64, error) {
	normalized := utils.SanitizeServerURL(sourceURL)
	if normalized == "" {
		return 0, fmt.Errorf("server %q: static GTFS source URL cannot be normalized", serverName)
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureStaticGTFSSource(ctx, dbgen.EnsureStaticGTFSSourceParams{
			ServerID:            serverID,
			NormalizedSourceUrl: normalized,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("server %q: ensure static GTFS source: %w", serverName, err)
	}
	return id, nil
}

// ensureVehicle returns the ID of a vehicle within one vehicle-position feed.
func ensureVehicle(ctx context.Context, q *dbgen.Queries, endpointID int64, externalVehicleID string) (int64, error) {
	if externalVehicleID == "" {
		return 0, fmt.Errorf("ensure vehicle on endpoint %d: %w", endpointID, errEmptyID)
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureVehicleEntity(ctx, dbgen.EnsureVehicleEntityParams{
			EndpointID:        endpointID,
			ExternalVehicleID: externalVehicleID,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("ensure vehicle %q on endpoint %d: %w", externalVehicleID, endpointID, err)
	}
	return id, nil
}

// ensureReportedStop returns the ID of a stop ID that OBA reported for an
// agency.
func ensureReportedStop(ctx context.Context, q *dbgen.Queries, agencyID int64, obaStopID string) (int64, error) {
	if obaStopID == "" {
		return 0, fmt.Errorf("ensure reported stop for agency %d: %w", agencyID, errEmptyID)
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureReportedStop(ctx, dbgen.EnsureReportedStopParams{
			AgencyID:  agencyID,
			ObaStopID: obaStopID,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("ensure reported stop %q for agency %d: %w", obaStopID, agencyID, err)
	}
	return id, nil
}

// ensureReportedTrip returns the ID of a trip ID that OBA reported for an
// agency.
func ensureReportedTrip(ctx context.Context, q *dbgen.Queries, agencyID int64, obaTripID string) (int64, error) {
	if obaTripID == "" {
		return 0, fmt.Errorf("ensure reported trip for agency %d: %w", agencyID, errEmptyID)
	}
	id, err := retryOnNoRows(func() (int64, error) {
		return q.EnsureReportedTrip(ctx, dbgen.EnsureReportedTripParams{
			AgencyID:  agencyID,
			ObaTripID: obaTripID,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("ensure reported trip %q for agency %d: %w", obaTripID, agencyID, err)
	}
	return id, nil
}
