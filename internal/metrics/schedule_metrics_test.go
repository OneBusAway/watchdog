package metrics

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestReportScheduledServiceEmitsUnavailableInsteadOfOutsideService(t *testing.T) {
	server := models.ObaServer{ServerName: "schedule-test", AgencyID: "agency-schedule", AgencyName: "Schedule Agency", ObaBaseURL: "https://schedule-metrics.example"}
	service := &MetricsService{StaticStore: gtfs.NewStaticStore()}

	service.ReportScheduledService(time.Now(), server)
	labels := map[string]string{
		"agency_id": server.AgencyID, "agency_name": server.AgencyName,
		"server_name": server.ServerName, "server_url": server.ObaBaseURL,
	}
	available, err := getMetricValue(GtfsScheduleAvailable, labels)
	if err != nil {
		t.Fatal(err)
	}
	active, err := getMetricValue(GtfsScheduledServiceActive, labels)
	if err != nil {
		t.Fatal(err)
	}
	if available != 0 || active != 0 {
		t.Fatalf("unknown schedule must emit available=0 active=0, got available=%v active=%v", available, active)
	}

	DeleteSeriesForServer(server.ObaBaseURL)
	if len(seriesMatching(GtfsScheduleAvailable, map[string]string{"server_url": server.ObaBaseURL})) != 0 ||
		len(seriesMatching(GtfsScheduledServiceActive, map[string]string{"server_url": server.ObaBaseURL})) != 0 {
		t.Fatal("schedule metric series were not retired with the server")
	}
}
