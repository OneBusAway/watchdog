package metrics

import (
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

func (ms *MetricsService) CountVehiclePositions(server models.ObaServer) error {
	_, err := countVehiclePositions(server, ms.RealtimeStore)
	return err
}

func (ms *MetricsService) CountActiveVehiclesForAgency(server models.ObaServer) error {
	_, err := countActiveVehiclesForAgency(ms.NewObaClient(server), server)
	return err
}

func (ms *MetricsService) ReportTrackedAgencies(servers []models.ObaServer) {
	reportTrackedAgencies(servers)
}

func (ms *MetricsService) CheckBundleExpiration(currentTime time.Time, server models.ObaServer) (int, int, error) {
	return checkBundleExpiration(ms.StaticStore, currentTime, server)
}

func (ms *MetricsService) ServerPing(server models.ObaServer) bool {
	return serverPing(ms.NewObaClient(server), server)
}

func (ms *MetricsService) FetchObaAPIMetrics(agencyID, agencyName, serverName, serverBaseURL, apiKey string) error {
	return fetchObaAPIMetrics(agencyID, agencyName, serverName, serverBaseURL, apiKey, ms.Client, ms.StaticStore, ms.Logger, ms.UnmatchedStopTracker)
}

func (ms *MetricsService) TrackVehicleTelemetry(server models.ObaServer) error {
	return trackVehicleTelemetry(server, ms.VehicleLastSeen, ms.RealtimeStore, ms.RouteAgencyIndex)
}

func (ms *MetricsService) TrackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer) error {
	return trackInvalidVehiclesAndStoppedOutOfBounds(server, ms.BoundingBoxStore, ms.RealtimeStore, ms.RouteAgencyIndex)
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
