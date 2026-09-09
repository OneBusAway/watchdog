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
	StaticStore      *StaticStore
	RealtimeStore    *RealtimeStore
	BoundingBoxStore *geo.BoundingBoxStore
	RouteAgencyIndex *RouteAgencyIndex
	Logger           *slog.Logger
	Client           *http.Client
	Observer         StaticBundleObserver
}

func NewGtfsService(staticStore *StaticStore, realtimeStore *RealtimeStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *RouteAgencyIndex, logger *slog.Logger, client *http.Client) *GtfsService {
	return &GtfsService{
		StaticStore:      staticStore,
		RealtimeStore:    realtimeStore,
		BoundingBoxStore: boundingBoxStore,
		RouteAgencyIndex: routeAgencyIndex,
		Logger:           logger,
		Client:           client,
	}
}

// SetBundleObserver registers a callback that fires once per (server, agency)
// tuple after each static bundle is stored. Used by the metrics layer to
// emit introspection gauges without creating an import cycle.
func (gs *GtfsService) SetBundleObserver(observer StaticBundleObserver) {
	gs.Observer = observer
}

func (gs *GtfsService) DownloadGTFSBundles(ctx context.Context, servers []models.ObaServer, maxRetries int) {
	downloadGTFSBundles(ctx, gs.Client, servers, gs.Logger, gs.BoundingBoxStore, gs.StaticStore, gs.RouteAgencyIndex, gs.Observer, maxRetries)
}

func (gs *GtfsService) DownloadGTFSBundle(ctx context.Context, url, agencyID string, maxRetires int) (*remoteGtfs.Static, error) {
	return downloadGTFSBundle(ctx, gs.Client, url, agencyID, maxRetires)
}

// RefreshGTFSBundles re-downloads every configured server's static bundles on
// a fixed interval. servers is consulted on each tick so the routine follows
// configuration changes instead of the boot-time server list.
func (gs *GtfsService) RefreshGTFSBundles(ctx context.Context, servers func() []models.ObaServer, interval time.Duration, maxRetries int) {
	refreshGTFSBundles(ctx, gs.Client, servers, gs.Logger, interval, gs.BoundingBoxStore, gs.StaticStore, gs.RouteAgencyIndex, gs.Observer, maxRetries)
}

func (gs *GtfsService) FetchAndStoreGTFSRTFeed(ctx context.Context, server models.ObaServer) error {
	return fetchAndStoreGTFSRTFeed(ctx, server, gs.RealtimeStore, gs.Client)
}

// exported helper functions
func GetEarliestAndLatestServiceDates(staticData *models.StaticData) (earliest, latest time.Time, err error) {
	earliestTime, latestTime, err := getEarliestAndLatestServiceDates(staticData)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return earliestTime, latestTime, nil
}

func GetStopLocationsByIDs(serverKey string, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	return getStopLocationsByIDs(serverKey, stopIDs, staticStore)
}
