package gtfs

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	remoteGtfs "github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

type GtfsService struct {
	StaticStore      *StaticStore
	RealtimeStore    *RealtimeStore
	BoundingBoxStore *geo.BoundingBoxStore
	Logger           *slog.Logger
	Client           *http.Client
}

func NewGtfsService(staticStore *StaticStore, realtimeStore *RealtimeStore, boundingBoxStore *geo.BoundingBoxStore, logger *slog.Logger, client *http.Client) *GtfsService {
	return &GtfsService{
		StaticStore:      staticStore,
		RealtimeStore:    realtimeStore,
		BoundingBoxStore: boundingBoxStore,
		Logger:           logger,
		Client:           client,
	}
}

func (gs *GtfsService) DownloadGTFSBundles(ctx context.Context, servers []models.ObaServer, maxRetries int) {
	downloadGTFSBundles(ctx, servers, gs.Logger, gs.BoundingBoxStore, gs.StaticStore, maxRetries)
}

// This service method downloads a GTFS static bundle from the provided URL,
// currently we uses (DownloadGTFSBundles) to fetch GTFS data for all servers.
// which internally calls downloadAndStoreGTFSBundle for each server.
// but this public method can be used to download a single GTFS bundle.
// It parses the GTFS data and stores it in the StaticStore using the serverID as the key.
// It returns an error if the download or parsing fails.
func (gs *GtfsService) DownloadGTFSBundle(ctx context.Context, url string, serverID int, maxRetires int) (*remoteGtfs.Static, error) {
	return downloadGTFSBundle(ctx, url, serverID, maxRetires)
}

func (gs *GtfsService) StoreGTFSBundle(staticBundle *remoteGtfs.Static, serverID int) error {
	return storeGTFSBundle(staticBundle, serverID, gs.StaticStore, gs.BoundingBoxStore)
}

func (gs *GtfsService) RefreshGTFSBundles(ctx context.Context, servers []models.ObaServer, interval time.Duration, maxRetries int) {
	refreshGTFSBundles(ctx, servers, gs.Logger, interval, gs.BoundingBoxStore, gs.StaticStore, maxRetries)
}

func (gs *GtfsService) FetchAndStoreGTFSRTFeed(server models.ObaServer) error {
	return fetchAndStoreGTFSRTFeed(server, gs.RealtimeStore, gs.Client)
}

// exported helper functions
func GetEarliestAndLatestServiceDates(staticData *models.StaticData) (earliest, latest time.Time, err error) {
	earliestTime, latestTime, err := getEarliestAndLatestServiceDates(staticData)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return earliestTime, latestTime, nil
}

func GetStopLocationsByIDs(serverID int, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	return getStopLocationsByIDs(serverID, stopIDs, staticStore)
}
