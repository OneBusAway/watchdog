package utils

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/report"
)

func GetLastCachedFile(cacheDir string, serverID int) (string, error) {
	files, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", err
	}

	var lastModTime time.Time
	var lastModFile string

	serverPrefix := fmt.Sprintf("server_%d_", serverID)

	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), serverPrefix) {
			fileInfo, err := file.Info()
			if err != nil {
				return "", err
			}
			if fileInfo.ModTime().After(lastModTime) {
				lastModTime = fileInfo.ModTime()
				lastModFile = file.Name()
			}
		}
	}

	if lastModFile == "" {
		return "", fmt.Errorf("no cached files found for server %d", serverID)
	}

	return filepath.Join(cacheDir, lastModFile), nil
}

// CreateCacheDirectory ensures the cache directory exists, creating it if necessary.
func CreateCacheDirectory(cacheDir string, logger *slog.Logger) error {
	stat, err := os.Stat(cacheDir)

	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(cacheDir, os.ModePerm); err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Level: sentry.LevelError,
					ExtraContext: map[string]interface{}{
						"cache_dir": cacheDir,
					},
				})
				return err
			}
			return nil
		}
		return err

	}
	if !stat.IsDir() {
		err := fmt.Errorf("%s is not a directory", cacheDir)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Level: sentry.LevelError,
			ExtraContext: map[string]interface{}{
				"cache_dir": cacheDir,
			},
		})
		return err
	}
	return nil
}
