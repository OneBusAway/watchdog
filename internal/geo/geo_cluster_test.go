package geo

import (
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

const (
	testLat = 38.8895 // ~Union Station, Washington DC
	testLon = -77.0352
)

func floatPtr(v float64) *float64 { return &v }

func TestGetClusterID_StationChild(t *testing.T) {
	station := remoteGtfs.Stop{Id: "station-1", Type: remoteGtfs.StopType_Station}
	platform := remoteGtfs.Stop{
		Id:        "platform-1",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &station,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(platform)
	if !ok {
		t.Fatal("expected valid cluster for platform with station parent")
	}
	if cluster.StationID != "station-1" {
		t.Fatalf("expected station id station-1, got %q", cluster.StationID)
	}
	if cluster.ID[:3] != "s2_" {
		t.Fatalf("expected s2_ cluster id prefix, got %q", cluster.ID)
	}
}

func TestGetClusterID_StationMembershipDoesNotChangeGeometry(t *testing.T) {
	station := remoteGtfs.Stop{Id: "station-1", Type: remoteGtfs.StopType_Station}
	withStation := remoteGtfs.Stop{
		Id:        "platform-1",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &station,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}
	standalone := remoteGtfs.Stop{
		Id:        "platform-1",
		Type:      remoteGtfs.StopType_Stop,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	inStation, okA := getClusterID(withStation)
	alone, okB := getClusterID(standalone)
	if !okA || !okB {
		t.Fatal("both stops should produce valid clusters")
	}
	if inStation.ID != alone.ID {
		t.Errorf("cluster id depends on station membership: %q vs %q", inStation.ID, alone.ID)
	}
	if inStation.Latitude != alone.Latitude || inStation.Longitude != alone.Longitude {
		t.Errorf("cluster center depends on station membership: %+v vs %+v", inStation, alone)
	}
	if inStation.StationID == alone.StationID {
		t.Errorf("expected different station ids, got %q for both", inStation.StationID)
	}
}

func TestGetClusterID_StandaloneStop(t *testing.T) {
	stop := remoteGtfs.Stop{
		Id:        "platform-2",
		Type:      remoteGtfs.StopType_Stop,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(stop)
	if !ok {
		t.Fatal("expected valid cluster for standalone stop")
	}
	if cluster.StationID != NoStationID {
		t.Fatalf("expected station id %q, got %q", NoStationID, cluster.StationID)
	}
}

func TestGetClusterID_Station(t *testing.T) {
	station := remoteGtfs.Stop{
		Id:        "station-2",
		Type:      remoteGtfs.StopType_Station,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(station)
	if !ok {
		t.Fatal("expected valid cluster for a station")
	}
	if cluster.StationID != "station-2" {
		t.Fatalf("expected station id station-2, got %q", cluster.StationID)
	}
}

func TestGetClusterID_EntranceExit(t *testing.T) {
	station := remoteGtfs.Stop{Id: "station-3", Type: remoteGtfs.StopType_Station}
	entrance := remoteGtfs.Stop{
		Id:        "entrance-1",
		Type:      remoteGtfs.StopType_EntranceOrExit,
		Parent:    &station,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(entrance)
	if !ok {
		t.Fatal("expected valid cluster for entrance")
	}
	if cluster.StationID != "station-3" {
		t.Fatalf("expected station id station-3, got %q", cluster.StationID)
	}
}

func TestGetClusterID_BoardingAreaWithGrandparent(t *testing.T) {
	station := remoteGtfs.Stop{Id: "station-4", Type: remoteGtfs.StopType_Station}
	platform := remoteGtfs.Stop{
		Id:        "platform-4",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &station,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}
	boarding := remoteGtfs.Stop{
		Id:        "boarding-1",
		Type:      remoteGtfs.StopType_BoardingArea,
		Parent:    &platform,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(boarding)
	if !ok {
		t.Fatal("expected valid cluster for boarding area with grandparent station")
	}
	if cluster.StationID != "station-4" {
		t.Fatalf("expected station id station-4, got %q", cluster.StationID)
	}
}

func TestGetClusterID_BoardingAreaWithoutGrandparent(t *testing.T) {
	platform := remoteGtfs.Stop{
		Id:        "platform-5",
		Type:      remoteGtfs.StopType_Stop,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}
	boarding := remoteGtfs.Stop{
		Id:        "boarding-2",
		Type:      remoteGtfs.StopType_BoardingArea,
		Parent:    &platform,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	cluster, ok := getClusterID(boarding)
	if !ok {
		t.Fatal("expected valid cluster for boarding area without grandparent")
	}
	if cluster.StationID != NoStationID {
		t.Fatalf("expected station id %q, got %q", NoStationID, cluster.StationID)
	}
}

func TestGetClusterID_TwoStationsInSameCell(t *testing.T) {
	stationA := remoteGtfs.Stop{Id: "station-a", Type: remoteGtfs.StopType_Station}
	stationB := remoteGtfs.Stop{Id: "station-b", Type: remoteGtfs.StopType_Station}

	// Two platforms with identical coordinates sit in the same S2 cell but
	// belong to different stations: they must not be merged into one series.
	platformA := remoteGtfs.Stop{
		Id:        "platform-a",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &stationA,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}
	platformB := remoteGtfs.Stop{
		Id:        "platform-b",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &stationB,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	clusterA, okA := getClusterID(platformA)
	clusterB, okB := getClusterID(platformB)
	if !okA || !okB {
		t.Fatal("expected both platforms to produce valid clusters")
	}
	if clusterA.ID != clusterB.ID {
		t.Fatalf("expected same cluster id, got %q vs %q", clusterA.ID, clusterB.ID)
	}
	if clusterA.StationID == clusterB.StationID {
		t.Fatalf("expected different station ids, got %q for both", clusterA.StationID)
	}
}

func TestGetClusterID_DeterministicAndNearStop(t *testing.T) {
	stop := remoteGtfs.Stop{
		Id:        "platform-6",
		Type:      remoteGtfs.StopType_Stop,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	first, okA := getClusterID(stop)
	second, okB := getClusterID(stop)
	if !okA || !okB {
		t.Fatal("expected valid cluster")
	}
	if first != second {
		t.Fatalf("expected deterministic cluster, got %+v then %+v", first, second)
	}
	if first.Latitude < -90 || first.Latitude > 90 || first.Longitude < -180 || first.Longitude > 180 {
		t.Fatalf("cluster center out of valid range: %+v", first)
	}
	// Level 13 cells span ~850-1225 m, so the center must be within ~1 km of the stop.
	if dist := haversineDistance(testLat, testLon, first.Latitude, first.Longitude); dist > 1200 {
		t.Fatalf("cluster center %.6f,%.6f is %.1f m away from stop", first.Latitude, first.Longitude, dist)
	}
}

func TestGetClusterID_MalformedHierarchy(t *testing.T) {
	platformParent := remoteGtfs.Stop{Id: "platform-x", Type: remoteGtfs.StopType_Stop}
	platform := remoteGtfs.Stop{
		Id:        "platform-7",
		Type:      remoteGtfs.StopType_Stop,
		Parent:    &platformParent,
		Latitude:  floatPtr(testLat),
		Longitude: floatPtr(testLon),
	}

	if _, ok := getClusterID(platform); ok {
		t.Fatal("expected malformed hierarchy (platform under platform) to be rejected")
	}
}

func TestGetClusterID_MissingCoordinates(t *testing.T) {
	stop := remoteGtfs.Stop{Id: "platform-8", Type: remoteGtfs.StopType_Stop}

	if _, ok := getClusterID(stop); ok {
		t.Fatal("expected stop without coordinates to be rejected")
	}
}
