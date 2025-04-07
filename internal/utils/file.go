package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
