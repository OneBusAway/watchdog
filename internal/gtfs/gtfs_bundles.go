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
	"time"

	"github.com/getsentry/sentry-go"
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
