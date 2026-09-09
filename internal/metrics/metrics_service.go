package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type MetricsService struct {
	StaticStore          *gtfs.StaticStore
	RealtimeStore        *gtfs.RealtimeStore
	BoundingBoxStore     *geo.BoundingBoxStore
	RouteAgencyIndex     *gtfs.RouteAgencyIndex
	VehicleLastSeen      *VehicleLastSeen
	UnmatchedStopTracker *UnmatchedStopTracker
	Logger               *slog.Logger
	Client               *http.Client
	NewObaClient         func(models.ObaServer) *onebusaway.Client
}

func NewMetricsService(static *gtfs.StaticStore, realtime *gtfs.RealtimeStore, bbox *geo.BoundingBoxStore, routeAgencyIndex *gtfs.RouteAgencyIndex, vehicleLastSeen *VehicleLastSeen, unmatchedStopTracker *UnmatchedStopTracker, logger *slog.Logger, client *http.Client, newObaClient func(models.ObaServer) *onebusaway.Client) *MetricsService {
	return &MetricsService{
		StaticStore:          static,
		RealtimeStore:        realtime,
		BoundingBoxStore:     bbox,
		RouteAgencyIndex:     routeAgencyIndex,
		VehicleLastSeen:      vehicleLastSeen,
		UnmatchedStopTracker: unmatchedStopTracker,
		Logger:               logger,
		Client:               client,
		NewObaClient:         newObaClient,
	}
}

// CountVehiclePositions reports the GTFS-RT vehicle-position count. Pass nil
// agencies for an agency-scoped entry; pass the live agency entries for a
// server-scoped one so the gauge is attributed per agency.
func (ms *MetricsService) CountVehiclePositions(server models.ObaServer, agencies []models.ObaServer) error {
	_, err := countVehiclePositions(server, agencies, ms.RealtimeStore, ms.RouteAgencyIndex)
	return err
}

func (ms *MetricsService) CountActiveVehiclesForAgency(ctx context.Context, server models.ObaServer) error {
	_, err := countActiveVehiclesForAgency(ctx, ms.NewObaClient(server), server)
	return err
}

func (ms *MetricsService) ReportTrackedAgencies(servers []models.ObaServer) {
	reportTrackedAgencies(servers)
}

func (ms *MetricsService) CheckBundleExpiration(currentTime time.Time, server models.ObaServer) (int, int, error) {
	return checkBundleExpiration(ms.StaticStore, currentTime, server)
}

func (ms *MetricsService) ServerPing(ctx context.Context, server models.ObaServer) bool {
	return serverPing(ctx, ms.NewObaClient(server), server)
}

func (ms *MetricsService) FetchObaAPIMetrics(ctx context.Context, agencyID, agencyName, serverName, serverBaseURL, apiKey string) error {
	return fetchObaAPIMetrics(ctx, agencyID, agencyName, serverName, serverBaseURL, apiKey, ms.Client, ms.StaticStore, ms.Logger, ms.UnmatchedStopTracker)
}

// TrackVehicleTelemetry runs the per-vehicle telemetry pass exactly once per
// server per tick. Pass nil agencies for an agency-scoped entry; pass the live
// agency entries for a server-scoped one.
func (ms *MetricsService) TrackVehicleTelemetry(server models.ObaServer, agencies []models.ObaServer) error {
	return trackVehicleTelemetry(server, agencies, ms.VehicleLastSeen, ms.RealtimeStore, ms.RouteAgencyIndex)
}

// TrackInvalidVehiclesAndStoppedOutOfBounds reports coordinate validity and
// out-of-bounds counts. Pass nil agencies for an agency-scoped entry; pass the
// live agency entries for a server-scoped one.
func (ms *MetricsService) TrackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer, agencies []models.ObaServer) error {
	return trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, ms.BoundingBoxStore, ms.RealtimeStore, ms.RouteAgencyIndex)
}

// StaticBundleObserver returns a callback suitable for GtfsService.SetBundleObserver.
// It emits the per-agency introspection gauges (stop count, route count) when
// the gtfs layer has finished parsing and storing a bundle. Defined here
// rather than inside the gtfs package to avoid a metrics → gtfs → metrics
// import cycle.
func (ms *MetricsService) StaticBundleObserver() func(server models.ObaServer, agencyID, agencyName string, bundle *models.StaticData) {
	return func(server models.ObaServer, agencyID, agencyName string, bundle *models.StaticData) {
		serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
		GtfsStaticStopsCount.WithLabelValues(
			agencyID, agencyName, server.ServerName, serverURL,
		).Set(float64(len(bundle.Stops)))
		GtfsStaticRoutesCount.WithLabelValues(
			agencyID, agencyName, server.ServerName, serverURL,
		).Set(float64(len(bundle.Routes)))
	}
}
