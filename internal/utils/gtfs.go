package utils

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/getsentry/sentry-go"
)

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
