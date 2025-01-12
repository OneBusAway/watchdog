package utils

import (
	"io"
	"net/http"
	"os"
)


func DownloadGTFSBundle(url string, cachePath string) error {
	resp, err := http.Get(url)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	out, err := os.Create(cachePath)
	if err != nil {
		return err
	}

	defer out.Close()


	_, err = io.Copy(out, resp.Body)

	return err;
}
