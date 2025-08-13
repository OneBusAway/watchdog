package metrics

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	remoteGtfs "github.com/jamespfennell/gtfs"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
	"watchdog.onebusaway.org/internal/gtfs"
)

func TestFetchObaAPIMetrics_WithVCR(t *testing.T) {
	data := readFixture(t, "gtfs.zip")
	staticData, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticStore := gtfs.NewStaticStore()
	
	tests := []struct {
		name      string
		slugID    string
		serverID  int
		serverURL string
		apiKey    string
		useVCR    bool
		cassette  string
		wantErr   bool
		errString string
	}{
		{
			name:      "successful request",
			slugID:    "unitrans",
			serverID:  1,
			serverURL: "https://oba-api.onrender.com",
			apiKey:    "org.onebusaway.iphone",
			useVCR:    true,
			cassette:  "oba_metrics_api_successful_request",
			wantErr:   false,
		},
		{
			name:      "not found error",
			slugID:    "invalid-region",
			serverID:  2,
			serverURL: "https://api.pugetsound.onebusaway.org",
			apiKey:    "org.onebusaway.iphone",
			useVCR:    false,
			wantErr:   true,
			errString: "does not support metrics API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *http.Client

			if tt.useVCR {
				rec, err := recorder.New(filepath.Join("testdata", "vcr", tt.cassette))
				if err != nil {
					t.Fatalf("Failed to create recorder: %v", err)
				}
				defer rec.Stop()

				client = &http.Client{
					Transport: rec,
					Timeout:   10 * time.Second,
				}
			}
			staticStore.Set(tt.serverID, staticData)
			err := fetchObaAPIMetrics(tt.slugID, tt.serverID, tt.serverURL, tt.apiKey, client,staticStore)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
					return
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
