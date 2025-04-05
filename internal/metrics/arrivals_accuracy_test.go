package metrics

import (
	"testing"
	"watchdog.onebusaway.org/internal/models"
)

// Mock server configuration
var mockServer = models.ObaServer{
	ObaApiKey:  "org.onebusaway.iphone",
	ObaBaseURL: "https://api.pugetsound.onebusaway.org/",
	AgencyID:   "40",
}

// **Test getStops()**
func TestGetStops(t *testing.T) {
	stops, err := getStops(mockServer)
	if err != nil {
		t.Fatalf("getStops failed: %v", err)
	}

	if len(stops) == 0 {
		t.Fatalf("getStops returned empty data, possible API failure")
	}

	t.Logf("Successfully retrieved %d stops", len(stops))
}

// **Test CheckArrivalsAccuracy()**
func TestCheckArrivalsAccuracy(t *testing.T) {
	predictedRatio, perfectPredictionRate, err := CheckArrivalsAccuracy(mockServer)
	if err != nil {
		t.Fatalf("CheckArrivalsAccuracy failed: %v", err)
	}

	// Predicted ratio should be between 0 and 1
	if predictedRatio < 0 || predictedRatio > 1 {
		t.Errorf("Invalid predicted ratio: %f", predictedRatio)
	}

	// Perfect prediction rate should be between 0 and 1
	if perfectPredictionRate < 0 || perfectPredictionRate > 1 {
		t.Errorf("Invalid perfect prediction rate: %f", perfectPredictionRate)
	}

	t.Logf("Predicted ratio: %.2f, Perfect prediction rate: %.2f", predictedRatio, perfectPredictionRate)
}
