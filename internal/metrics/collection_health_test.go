package metrics

import (
	"testing"
	"time"
)

func TestReportCollectionHealth(t *testing.T) {
	service := &MetricsService{}
	completedAt := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)

	service.ReportCollectionInterval(30 * time.Second)
	service.ReportCollectionCompleted(completedAt)

	got, err := gaugeValue(WatchdogCollectionInterval)
	if err != nil {
		t.Fatal(err)
	}
	if got != 30 {
		t.Fatalf("collection interval = %v, want 30", got)
	}
	got, err = gaugeValue(WatchdogCollectionLastCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(completedAt.Unix()) {
		t.Fatalf("last completed = %v, want %v", got, completedAt.Unix())
	}
}
