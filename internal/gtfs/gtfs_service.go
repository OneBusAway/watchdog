package gtfs

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

type GtfsService struct {
	StaticStore        *StaticStore
	RealtimeStore      *RealtimeStore
	BoundingBoxStore   *geo.BoundingBoxStore
	RouteAgencyIndex   *RouteAgencyIndex
	Logger             *slog.Logger
	Client             *http.Client
	Observer           StaticBundleObserver
	MappingObserver    StaticFeedMappingObserver
	RefreshObserver    StaticRefreshObserver
	FeedObserver       StaticFeedRefreshObserver
	refreshCoordinator *staticRefreshCoordinator
}

type StaticRefreshObservation struct {
	AttemptedAt time.Time
	Success     bool
	Retrying    bool
	GaveUp      bool
}

type StaticRefreshObserver func(server models.ObaServer, observation StaticRefreshObservation)

type StaticFeedRefreshObservation struct {
	AttemptedAt  time.Time
	Success      bool
	FailureSince time.Time
	Stage        StaticFailureStage
	Reason       StaticFailureReason
	Conservative bool
	ContentHash  string
}

type StaticFeedRefreshObserver func(server models.ObaServer, feedURL string, observation StaticFeedRefreshObservation)

func NewGtfsService(staticStore *StaticStore, realtimeStore *RealtimeStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *RouteAgencyIndex, logger *slog.Logger, client *http.Client) *GtfsService {
	return &GtfsService{
		StaticStore:        staticStore,
		RealtimeStore:      realtimeStore,
		BoundingBoxStore:   boundingBoxStore,
		RouteAgencyIndex:   routeAgencyIndex,
		Logger:             logger,
		Client:             client,
		refreshCoordinator: newStaticRefreshCoordinator(),
	}
}

// SetBundleObserver registers a callback that fires once per (server, agency)
// tuple after each static bundle is stored. Used by the metrics layer to
// emit introspection gauges without creating an import cycle.
func (gs *GtfsService) SetBundleObserver(observer StaticBundleObserver) {
	gs.Observer = observer
}

func (gs *GtfsService) SetFeedMappingObserver(observer StaticFeedMappingObserver) {
	gs.MappingObserver = observer
}

func (gs *GtfsService) SetStaticRefreshObserver(observer StaticRefreshObserver) {
	gs.RefreshObserver = observer
}

func (gs *GtfsService) SetStaticFeedRefreshObserver(observer StaticFeedRefreshObserver) {
	gs.FeedObserver = observer
}

func (gs *GtfsService) reportStaticFeedRefresh(server models.ObaServer, feedURL string, observation StaticFeedRefreshObservation) {
	if gs.FeedObserver == nil || !gs.StaticStore.IsConfigured(server) {
		return
	}
	defer func() { _ = recover() }()
	gs.FeedObserver(server, feedURL, observation)
}

func (gs *GtfsService) reportStaticRefresh(server models.ObaServer, observation StaticRefreshObservation) {
	gs.StaticStore.WithRefreshLock(func() {
		if !gs.StaticStore.IsConfigured(server) {
			return
		}
		gs.StaticStore.SetRefreshState(server, StaticRefreshState{
			LastAttempt: observation.AttemptedAt, Success: observation.Success,
			Retrying: observation.Retrying, GaveUp: observation.GaveUp,
		})
		if gs.RefreshObserver != nil {
			gs.RefreshObserver(server, observation)
		}
	})
}

func (gs *GtfsService) DownloadGTFSBundles(ctx context.Context, servers []models.ObaServer, maxRetries int) {
	for _, result := range downloadGTFSBundles(ctx, gs.Client, servers, gs.Logger, gs.BoundingBoxStore, gs.StaticStore, gs.RouteAgencyIndex, gs.Observer, gs.MappingObserver, maxRetries) {
		gs.reportStaticRefresh(result.Server, StaticRefreshObservation{AttemptedAt: result.AttemptedAt, Success: result.Err == nil})
	}
}

func (gs *GtfsService) StartStaticRefreshCampaigns(ctx context.Context, servers []models.ObaServer, trigger StaticRefreshTrigger, maxRetries int) {
	for _, server := range servers {
		if gs.StaticStore.IsConfigured(server) {
			gs.refreshCoordinator.start(ctx, gs, server, trigger, maxRetries)
		}
	}
}

func (gs *GtfsService) ReconcileStaticRefreshCampaigns(servers []models.ObaServer) {
	gs.refreshCoordinator.reconcile(servers)
}

func (gs *GtfsService) ReconcileRealtimeConfiguration(server models.ObaServer) {
	gs.RealtimeStore.ReconcileConfiguration(server)
}

func (gs *GtfsService) DownloadGTFSBundle(ctx context.Context, url, agencyID string, maxRetires int) (*remoteGtfs.Static, error) {
	return downloadGTFSBundle(ctx, gs.Client, url, agencyID, maxRetires)
}

// RefreshGTFSBundles re-downloads every configured server's static bundles on
// a fixed interval. servers is consulted on each tick so the routine follows
// configuration changes instead of the boot-time server list.
func (gs *GtfsService) RefreshGTFSBundles(ctx context.Context, servers func() []models.ObaServer, interval time.Duration, maxRetries int) {
	startDailyStaticRefreshes(ctx, gs, servers, gs.Logger, interval, maxRetries)
}

// FetchAndStoreGTFSRTFeed stores a consolidated feed in server mode. In
// agency-mode it resolves route_id/trip_id through RouteAgencyIndex and stores
// only vehicles belonging to server.AgencyID.
func (gs *GtfsService) FetchAndStoreGTFSRTFeed(ctx context.Context, server models.ObaServer) error {
	return fetchAndStoreGTFSRTFeed(ctx, server, gs.RealtimeStore, gs.Client, gs.RouteAgencyIndex)
}

// exported helper functions
func GetEarliestAndLatestServiceDates(staticData *models.StaticData) (earliest, latest time.Time, err error) {
	earliestTime, latestTime, err := getEarliestAndLatestServiceDates(staticData)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return earliestTime, latestTime, nil
}

// GetStopLocationsByIDs resolves stop IDs from the stored static snapshot.
func GetStopLocationsByIDs(serverKey string, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	return getStopLocationsByIDs(serverKey, stopIDs, staticStore)
}
