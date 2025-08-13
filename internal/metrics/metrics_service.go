package metrics

import (
	"log/slog"
	"net/http"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

type MetricsService struct {
	StaticStore      *gtfs.StaticStore
	RealtimeStore    *gtfs.RealtimeStore
	BoundingBoxStore *geo.BoundingBoxStore
	VehicleLastSeen  *VehicleLastSeen
	Logger           *slog.Logger
	Client           *http.Client
}

func NewMetricsService(static *gtfs.StaticStore, realtime *gtfs.RealtimeStore, bbox *geo.BoundingBoxStore, vehicleLastSeen *VehicleLastSeen, logger *slog.Logger, client *http.Client) *MetricsService {
	return &MetricsService{
		StaticStore:      static,
		RealtimeStore:    realtime,
		BoundingBoxStore: bbox,
		VehicleLastSeen:  vehicleLastSeen,
		Logger:           logger,
		Client:           client,
	}
}

func (ms *MetricsService) CheckVehicleCountMatch(server models.ObaServer) error {
	return checkVehicleCountMatch(server, ms.RealtimeStore)
}

func (ms *MetricsService) CheckAgenciesWithCoverageMatch(server models.ObaServer) error {
	if err := checkAgenciesWithCoverageMatch(ms.StaticStore, ms.Logger, server); err != nil {
		return err
	}
	return nil

}

func (ms *MetricsService) CheckBundleExpiration(currentTime time.Time, server models.ObaServer) (int, int, error) {
	return checkBundleExpiration(ms.StaticStore, currentTime, server)
}

func (ms *MetricsService) ServerPing(server models.ObaServer) {
	serverPing(server)
}

func (ms *MetricsService) FetchObaAPIMetrics(slugID string, serverID int, serverBaseUrl string, apiKey string) error {
	return fetchObaAPIMetrics(slugID, serverID, serverBaseUrl, apiKey, ms.Client, ms.StaticStore)
}

func (ms *MetricsService) TrackVehicleTelemetry(server models.ObaServer) error {
	return trackVehicleTelemetry(server, ms.VehicleLastSeen, ms.RealtimeStore)
}

func (ms *MetricsService) TrackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer) error {
	return trackInvalidVehiclesAndStoppedOutOfBounds(server, ms.BoundingBoxStore, ms.RealtimeStore)
}
