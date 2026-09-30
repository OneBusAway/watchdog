package gtfs

import (
	"testing"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
)

func TestCanonicalEntityHashIsOrderIndependentAndExcludesHeader(t *testing.T) {
	entityA := &gtfsrt.FeedEntity{Id: proto.String("a"), Vehicle: &gtfsrt.VehiclePosition{Vehicle: &gtfsrt.VehicleDescriptor{Id: proto.String("va")}}}
	entityB := &gtfsrt.FeedEntity{Id: proto.String("b"), Vehicle: &gtfsrt.VehiclePosition{Vehicle: &gtfsrt.VehicleDescriptor{Id: proto.String("vb")}}}

	forward := canonicalEntityHash([]*gtfsrt.FeedEntity{entityA, entityB})
	reversed := canonicalEntityHash([]*gtfsrt.FeedEntity{entityB, entityA})
	if forward != reversed {
		t.Fatalf("entity order changed canonical hash: %q != %q", forward, reversed)
	}

	// FeedHeader is intentionally not accepted by canonicalEntityHash, so a
	// source timestamp advance cannot alter this payload fingerprint.
	changed := proto.Clone(entityA).(*gtfsrt.FeedEntity)
	changed.Vehicle.StopId = proto.String("stop-2")
	if forward == canonicalEntityHash([]*gtfsrt.FeedEntity{changed, entityB}) {
		t.Fatal("semantic payload change did not alter canonical hash")
	}
}

func TestVehicleStateHashExcludesSourceTimestamp(t *testing.T) {
	entity := &gtfsrt.FeedEntity{Vehicle: &gtfsrt.VehiclePosition{
		Vehicle: &gtfsrt.VehicleDescriptor{Id: proto.String("v")}, Timestamp: proto.Uint64(100),
	}}
	first := vehicleStateHashes([]*gtfsrt.FeedEntity{entity})["v"][0]
	entity.Vehicle.Timestamp = proto.Uint64(200)
	second := vehicleStateHashes([]*gtfsrt.FeedEntity{entity})["v"][0]
	if first != second {
		t.Fatalf("source timestamp altered semantic vehicle hash: %q != %q", first, second)
	}
	entity.Vehicle.StopId = proto.String("new-stop")
	if first == vehicleStateHashes([]*gtfsrt.FeedEntity{entity})["v"][0] {
		t.Fatal("semantic vehicle change did not alter state hash")
	}
}
