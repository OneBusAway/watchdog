package metrics

import (
	"log/slog"
	"net/http"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

type MetricsService struct {
	StaticStore          *gtfs.StaticStore
	RealtimeStore        *gtfs.RealtimeStore
	BoundingBoxStore     *geo.BoundingBoxStore
	VehicleLastSeen      *VehicleLastSeen
	UnmatchedStopTracker *UnmatchedStopTracker
	Logger               *slog.Logger
	Client               *http.Client
	NewObaClient         func(models.ObaServer) *onebusaway.Client
}

func NewMetricsService(static *gtfs.StaticStore, realtime *gtfs.RealtimeStore, bbox *geo.BoundingBoxStore, vehicleLastSeen *VehicleLastSeen, unmatchedStopTracker *UnmatchedStopTracker, logger *slog.Logger, client *http.Client, newObaClient func(models.ObaServer) *onebusaway.Client) *MetricsService {
	return &MetricsService{
		StaticStore:          static,
		RealtimeStore:        realtime,
		BoundingBoxStore:     bbox,
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

func (ms *MetricsService) FetchObaAPIMetrics(agencyID, agencyName, serverBaseURL, apiKey string) error {
	return fetchObaAPIMetrics(agencyID, agencyName, serverBaseURL, apiKey, ms.Client, ms.StaticStore, ms.Logger, ms.UnmatchedStopTracker)
}

func (ms *MetricsService) TrackVehicleTelemetry(server models.ObaServer) error {
	return trackVehicleTelemetry(server, ms.VehicleLastSeen, ms.RealtimeStore)
}

func (ms *MetricsService) TrackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer) error {
	return trackInvalidVehiclesAndStoppedOutOfBounds(server, ms.BoundingBoxStore, ms.RealtimeStore)
}
