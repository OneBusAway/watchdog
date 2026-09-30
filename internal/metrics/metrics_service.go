package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/prometheus/client_golang/prometheus"
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
	FeedFreshness        *FeedFreshnessStore
	StaticFeedMappings   *StaticFeedMappingStore
	StaticFeedHealth     *StaticFeedHealthStore
	UnmatchedStopTracker *UnmatchedStopTracker
	Logger               *slog.Logger
	Client               *http.Client
	NewObaClient         func(models.ObaServer) *onebusaway.Client
}

const staticBundleCurrentMaxAge = 26 * time.Hour

func NewMetricsService(static *gtfs.StaticStore, realtime *gtfs.RealtimeStore, bbox *geo.BoundingBoxStore, routeAgencyIndex *gtfs.RouteAgencyIndex, vehicleLastSeen *VehicleLastSeen, unmatchedStopTracker *UnmatchedStopTracker, logger *slog.Logger, client *http.Client, newObaClient func(models.ObaServer) *onebusaway.Client) *MetricsService {
	feedFreshness := NewFeedFreshnessStore()
	staticFeedMappings := NewStaticFeedMappingStore()
	staticFeedHealth := NewStaticFeedHealthStore()
	realtime.SetObserver(feedFreshness.Observe)
	realtime.SetConfigurationObserver(func(server models.ObaServer) {
		removedFeeds := feedFreshness.Reconcile(server)
		vehicleLastSeen.RemoveFeeds(server, removedFeeds)
		for _, feedID := range removedFeeds {
			labels := prometheus.Labels{"feed": feedID, "server_url": utils.SanitizeServerURL(server.ObaBaseURL)}
			GtfsRtVehicleSourceTimestampAdvances.DeletePartialMatch(labels)
			GtfsRtVehicleStateChanges.DeletePartialMatch(labels)
		}
	})
	return &MetricsService{
		StaticStore:          static,
		RealtimeStore:        realtime,
		BoundingBoxStore:     bbox,
		RouteAgencyIndex:     routeAgencyIndex,
		VehicleLastSeen:      vehicleLastSeen,
		FeedFreshness:        feedFreshness,
		StaticFeedMappings:   staticFeedMappings,
		StaticFeedHealth:     staticFeedHealth,
		UnmatchedStopTracker: unmatchedStopTracker,
		Logger:               logger,
		Client:               client,
		NewObaClient:         newObaClient,
	}
}

func (ms *MetricsService) ReportCollectionInterval(interval time.Duration) {
	WatchdogCollectionInterval.Set(interval.Seconds())
	if ms.VehicleLastSeen != nil {
		ms.VehicleLastSeen.SetCollectionInterval(interval)
	}
}

func (ms *MetricsService) ReportCollectionCompleted(completedAt time.Time) {
	WatchdogCollectionLastCompleted.Set(float64(completedAt.UTC().Unix()))
}

func (ms *MetricsService) StaticFeedMappingObserver() gtfs.StaticFeedMappingObserver {
	return ms.StaticFeedMappings.Observe
}

func (ms *MetricsService) StaticRefreshObserver() gtfs.StaticRefreshObserver {
	return func(server models.ObaServer, observation gtfs.StaticRefreshObservation) {
		labels := []string{server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)}
		GtfsStaticRefreshLastAttempt.WithLabelValues(labels...).Set(float64(observation.AttemptedAt.UTC().Unix()))
		GtfsStaticRefreshLastAttemptSuccess.WithLabelValues(labels...).Set(boolValue(observation.Success))
		GtfsStaticRefreshRetrying.WithLabelValues(labels...).Set(boolValue(observation.Retrying))
		GtfsStaticRefreshGaveUp.WithLabelValues(labels...).Set(boolValue(observation.GaveUp))
	}
}

func (ms *MetricsService) StaticFeedRefreshObserver() gtfs.StaticFeedRefreshObserver {
	return ms.StaticFeedHealth.Observe
}

func (ms *MetricsService) ReconcileStaticFeedHealth(servers []models.ObaServer) {
	ms.StaticFeedHealth.Reconcile(servers)
}

// ReportScheduledService evaluates the compact immutable schedule snapshot at
// collection time. It never reparses static data.
func (ms *MetricsService) ReportScheduledService(now time.Time, server models.ObaServer) {
	ms.reportStaticBundleState(now, server)
	available, active := ms.StaticStore.ScheduleStore().Evaluate(server.ServerKey(), now)
	labels := []string{server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)}
	GtfsScheduleAvailable.WithLabelValues(labels...).Set(boolValue(available))
	GtfsScheduledServiceActive.WithLabelValues(labels...).Set(boolValue(active))
}

func (ms *MetricsService) reportStaticBundleState(now time.Time, server models.ObaServer) {
	labels := []string{server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)}
	current, usable := ms.StaticStore.BundleState(server, now, staticBundleCurrentMaxAge)
	GtfsStaticBundleUsable.WithLabelValues(labels...).Set(boolValue(usable))
	GtfsStaticBundleCurrent.WithLabelValues(labels...).Set(boolValue(current))
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
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

func (ms *MetricsService) FetchObaAPIMetrics(ctx context.Context, agencyID, agencyName, serverName, serverBaseURL, apiKey string, prefetchedMetrics *OBAMetrics) error {
	return fetchObaAPIMetrics(ctx, agencyID, agencyName, serverName, serverBaseURL, apiKey, ms.Client, ms.StaticStore, ms.Logger, ms.UnmatchedStopTracker, prefetchedMetrics)
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
		if bundle == nil {
			DeleteSeriesForAgency(serverURL, agencyID)
			return
		}
		GtfsStaticStopsCount.WithLabelValues(
			agencyID, agencyName, server.ServerName, serverURL,
		).Set(float64(len(bundle.Stops)))
		GtfsStaticRoutesCount.WithLabelValues(
			agencyID, agencyName, server.ServerName, serverURL,
		).Set(float64(len(bundle.Routes)))
	}
}
