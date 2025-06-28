package gtfs

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// DownloadGTFSBundles downloads GTFS bundles for each server and caches them locally.
func DownloadGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger) {
	for _, server := range servers {
		hash := sha1.Sum([]byte(server.GtfsUrl))
		hashStr := hex.EncodeToString(hash[:])
		cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", server.ID, hashStr))

		_, err := DownloadGTFSBundle(server.GtfsUrl, cacheDir, server.ID, hashStr)
		if err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("server_id", fmt.Sprintf("%d", server.ID)),
				ExtraContext: map[string]interface{}{
					"gtfs_url": server.GtfsUrl,
				},
				Level: sentry.LevelError,
			})
			logger.Error("Failed to download GTFS bundle", "server_id", server.ID, "error", err)
		} else {
			logger.Info("Successfully downloaded GTFS bundle", "server_id", server.ID, "path", cachePath)
		}
	}
}

// RefreshGTFSBundles periodically downloads GTFS bundles at the specified interval.
func RefreshGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger, interval time.Duration) {
	for {
		time.Sleep(interval)
		DownloadGTFSBundles(servers, cacheDir, logger)
	}
}

func DownloadGTFSBundle(url string, cacheDir string, serverID int, hashStr string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		sentry.CaptureException(err)
		return "", err
	}
	defer resp.Body.Close()

	cacheFileName := fmt.Sprintf("server_%d_%s.zip", serverID, hashStr)
	cachePath := filepath.Join(cacheDir, cacheFileName)

	out, err := os.Create(cachePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	return cachePath, nil
}

// ParseGTFSFromCache reads a GTFS bundle from the cache and parses it into a gtfs.Static object.
// It returns the parsed static data or an error if parsing fails.
func ParseGTFSFromCache(cachePath string, serverID int) (*gtfs.Static, error) {
	file, err := os.Open(cachePath)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}
	defer file.Close()

	// Convert the file into a byte slice
	fileInfo, err := file.Stat()
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}

	fileBytes := make([]byte, fileInfo.Size())
	if _, err = file.Read(fileBytes); err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}

	staticData, err := gtfs.ParseStatic(fileBytes, gtfs.ParseStaticOptions{})
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}

	return staticData, nil
}

// GetStopLocationsByIDs retrieves stop locations by their IDs from the GTFS cache.
// It returns a map of stop IDs to gtfs.Stop objects.

func GetStopLocationsByIDs(cachePath string, serverID int, stopIDs []string) (map[string]gtfs.Stop, error) {
	staticData, err := ParseGTFSFromCache(cachePath, serverID)
	if err != nil {
		return nil, err
	}

	stopIDSet := make(map[string]struct{}, len(stopIDs))
	for _, id := range stopIDs {
		stopIDSet[id] = struct{}{}
	}

	result := make(map[string]gtfs.Stop)
	for _, stop := range staticData.Stops {
		if _, exists := stopIDSet[stop.Id]; exists {
			result[stop.Id] = stop
		}
	}
	return result, nil
}
