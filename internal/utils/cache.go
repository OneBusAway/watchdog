package utils

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/report"
)

// GetLastCachedFile returns the most recently modified cache file for a given server ID
// within the specified cache directory. The cache file name is expected to follow
// the pattern "server_<ID>_...". Returns an error if no matching file is found
// or if reading the directory fails.
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

// CreateCacheDirectory ensures that the given cache directory exists.
// If the directory does not exist, it attempts to create it. If the path
// exists but is not a directory, it returns an error.
// Errors are reported to Sentry with contextual information.
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

// SaveMapToFile serializes the given map to a JSON file at the specified filepath.
// Returns an error if the file cannot be created or written to.
func SaveMapToFile[C comparable, V any](data map[C]V, filepath string) error {
	file, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(data)
}

// LoadMapFromFile reads a JSON-encoded map from the specified file and returns it.
// Returns an error if the file can't be opened or the contents can't be decoded.
func LoadMapFromFile[C comparable, V any](filepath string) (map[C]V, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var data map[C]V
	err = json.NewDecoder(file).Decode(&data)
	return data, err
}
