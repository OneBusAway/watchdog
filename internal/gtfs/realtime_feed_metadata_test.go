package gtfs

import (
	"context"
	"testing"
	"time"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/models"
)

// The published snapshot carries the same header time and payload hash as the
// feed observation, so a recorder can describe the response from the snapshot
// alone.
func TestRealtimeFeedCarriesHeaderTimeAndPayloadHash(t *testing.T) {
	headerTime := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		header         *gtfsrt.FeedHeader
		wantSourceTime *time.Time
		wantFull       bool
	}{
		{
			name: "header with timestamp",
			header: &gtfsrt.FeedHeader{
				GtfsRealtimeVersion: proto.String("2.0"),
				Timestamp:           proto.Uint64(uint64(headerTime.Unix())),
			},
			wantSourceTime: &headerTime,
			wantFull:       true,
		},
		{
			name: "differential header without timestamp",
			header: &gtfsrt.FeedHeader{
				GtfsRealtimeVersion: proto.String("2.0"),
				Incrementality:      gtfsrt.FeedHeader_DIFFERENTIAL.Enum(),
			},
			wantSourceTime: nil,
			wantFull:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feed := &gtfsrt.FeedMessage{
				Header: tt.header,
				Entity: []*gtfsrt.FeedEntity{{
					Id: proto.String("entity-1"),
					Vehicle: &gtfsrt.VehiclePosition{
						Vehicle:  &gtfsrt.VehicleDescriptor{Id: proto.String("bus-1")},
						Position: &gtfsrt.Position{Latitude: proto.Float32(47.6), Longitude: proto.Float32(-122.3)},
					},
				}},
			}
			data, err := proto.Marshal(feed)
			if err != nil {
				t.Fatal(err)
			}
			ts := serveBytes(t, data)
			defer ts.Close()

			server := models.ObaServer{ServerName: "metadata", ObaBaseURL: ts.URL, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL}}}
			store := NewRealtimeStore()
			var observation FeedObservation
			store.SetObserver(func(_ models.ObaServer, result FeedObservation) { observation = result })

			if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, ts.Client(), nil); err != nil {
				t.Fatalf("fetch failed: %v", err)
			}
			snapshot := store.Get(server.ServerKey())
			if snapshot == nil || len(snapshot.Feeds) != 1 {
				t.Fatalf("expected one feed in the snapshot, got %+v", snapshot)
			}
			got := snapshot.Feeds[0]

			if got.FullDataset != tt.wantFull {
				t.Errorf("FullDataset = %v, want %v", got.FullDataset, tt.wantFull)
			}
			switch {
			case tt.wantSourceTime == nil && got.SourceTimestamp != nil:
				t.Errorf("SourceTimestamp = %v, want nil", *got.SourceTimestamp)
			case tt.wantSourceTime != nil && (got.SourceTimestamp == nil || !got.SourceTimestamp.Equal(*tt.wantSourceTime)):
				t.Errorf("SourceTimestamp = %v, want %v", got.SourceTimestamp, *tt.wantSourceTime)
			}
			if got.PayloadHash == "" || got.PayloadHash != canonicalEntityHash(feed.Entity) {
				t.Errorf("PayloadHash = %q, want %q", got.PayloadHash, canonicalEntityHash(feed.Entity))
			}

			// The snapshot and the observation describe the same fetch.
			if got.PayloadHash != observation.PayloadHash {
				t.Errorf("snapshot hash %q differs from observation hash %q", got.PayloadHash, observation.PayloadHash)
			}
			if (got.SourceTimestamp == nil) != (observation.SourceTimestamp == nil) ||
				(got.SourceTimestamp != nil && !got.SourceTimestamp.Equal(*observation.SourceTimestamp)) {
				t.Errorf("snapshot header time %v differs from observation %v", got.SourceTimestamp, observation.SourceTimestamp)
			}
			if !snapshot.ObservationAt.Equal(observation.ObservedAt) {
				t.Errorf("ObservationAt = %v, want the observation's %v", snapshot.ObservationAt, observation.ObservedAt)
			}
		})
	}
}
