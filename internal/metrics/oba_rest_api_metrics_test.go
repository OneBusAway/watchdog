package metrics

import (
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchObaAPIMetrics_WithVCR(t *testing.T) {
	tests := []struct {
		name      string
		slugID    string
		useVCR    bool
		cassette  string
		wantErr   bool
		errString string
	}{
		{
			name:     "successful request",
			slugID:   "unitrans",
			useVCR:   true,
			cassette: "oba_metrics_api_successful_request",
			wantErr:  false,
		},
		{
			name:      "not found error",
			slugID:    "invalid-region",
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

			err := FetchObaAPIMetrics(tt.slugID, client)

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
