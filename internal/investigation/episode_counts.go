package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

const (
	episodeTypeUnmatchedStop      = "unmatched_stop"
	episodeTypeUnmatchedTrip      = "unmatched_trip"
	episodeTypeStaleSource        = "stale_source_timestamp"
	episodeTypeInvalidCoordinate  = "invalid_coordinate"
	episodeTypeStoppedOutOfBounds = "stopped_out_of_bounds"
)

var allowedEpisodeTypes = []string{
	episodeTypeUnmatchedStop,
	episodeTypeUnmatchedTrip,
	episodeTypeStaleSource,
	episodeTypeInvalidCoordinate,
	episodeTypeStoppedOutOfBounds,
}

var allowedGroupBys = []string{
	"agency",
	"endpoint",
	"opening_response",
	"closing_response",
	"static_found",
	"gtfs_static_version",
	"route_id",
	"cluster",
	"rule_version",
	"bounding_box_version",
	"position_validation_reason",
	"attribution_reason",
	"expected_schedule_state",
	"recurrence",
	"entity",
	"opened_hour",
	"opened_day",
}

var unmatchedCloseReasons = []string{
	"not_reported",
}

var vehicleCloseReasons = []string{
	"fresh_source_timestamp",
	"not_present_in_full_dataset",
	"coordinates_valid_again",
	"inside_bounding_box",
	"no_longer_stopped_at_stop",
}

func includableForGroupBy(groupBy *string, episodeType *string) map[string]resourceKind {
	if groupBy == nil {
		return map[string]resourceKind{}
	}
	switch *groupBy {
	case "agency":
		return map[string]resourceKind{"agency": kindAgency}
	case "endpoint":
		return map[string]resourceKind{"endpoint": kindEndpoint}
	case "opening_response":
		if episodeType != nil && (*episodeType == episodeTypeUnmatchedStop || *episodeType == episodeTypeUnmatchedTrip) {
			return map[string]resourceKind{"opening_response": kindOBAMetricsResponse}
		}
		return map[string]resourceKind{"opening_response": "vehicle_response"}
	case "closing_response":
		if episodeType != nil && (*episodeType == episodeTypeUnmatchedStop || *episodeType == episodeTypeUnmatchedTrip) {
			return map[string]resourceKind{"closing_response": kindOBAMetricsResponse}
		}
		return map[string]resourceKind{"closing_response": "vehicle_response"}
	case "gtfs_static_version":
		return map[string]resourceKind{"gtfs_static_version": "gtfs_static_version"}
	case "rule_version":
		return map[string]resourceKind{"rule_version": kindVehicleAnomalyRuleVersion}
	case "bounding_box_version":
		return map[string]resourceKind{"bounding_box_version": "bounding_box_version"}
	case "entity":
		return map[string]resourceKind{
			"reported_stop": kindReportedStop,
			"reported_trip": kindReportedTrip,
			"vehicle":       "vehicle",
		}
	default:
		return map[string]resourceKind{}
	}
}

var episodeCountsIncludable = map[string]resourceKind{
	"agency":               kindAgency,
	"endpoint":             kindEndpoint,
	"opening_response":     kindOBAMetricsResponse,
	"closing_response":     kindOBAMetricsResponse,
	"gtfs_static_version":  "gtfs_static_version",
	"rule_version":         kindVehicleAnomalyRuleVersion,
	"bounding_box_version": "bounding_box_version",
	"reported_stop":        kindReportedStop,
	"reported_trip":        kindReportedTrip,
	"vehicle":              "vehicle",
}

// episodeCountItem is the counts for one episode type (E41 ungrouped).
type episodeCountItem struct {
	EpisodeType string           `json:"episode_type"`
	Opened      int64            `json:"opened"`
	Closed      map[string]int64 `json:"closed"`
	OpenAtTo    int64            `json:"open_at_to"`
}

// groupedEpisodeCountItem is one group item in a grouped E41 response.
type groupedEpisodeCountItem struct {
	EpisodeType string           `json:"episode_type"`
	Value       any              `json:"value"`
	Opened      int64            `json:"opened"`
	Closed      map[string]int64 `json:"closed"`
	OpenAtTo    int64            `json:"open_at_to"`
	Links       links            `json:"links"`
}

// episodeCountsResponse is the E41 response body.
type episodeCountsResponse struct {
	From            apiTime  `json:"from"`
	To              apiTime  `json:"to"`
	ServerID        *int64   `json:"server_id"`
	AgencyID        *int64   `json:"agency_id"`
	EpisodeType     *string  `json:"episode_type,omitempty"`
	GroupBy         *string  `json:"group_by,omitempty"`
	Items           any      `json:"items"`
	GroupsTruncated *bool    `json:"groups_truncated,omitempty"`
	Included        included `json:"included,omitzero"`
	Links           links    `json:"links"`
}

// initialCloseReasons returns a map of allowed close reasons initialized to 0.
func initialCloseReasons(reasons []string) map[string]int64 {
	m := make(map[string]int64, len(reasons))
	for _, r := range reasons {
		m[r] = 0
	}
	return m
}

// isVehicleType checks if t is one of the vehicle anomaly codes.
func isVehicleType(t string) bool {
	return t == episodeTypeStaleSource || t == episodeTypeInvalidCoordinate || t == episodeTypeStoppedOutOfBounds
}

// validateDimensionType checks whether groupBy is compatible with episodeType.
func validateDimensionType(groupBy string, episodeType *string, bad *invalidParams) {
	switch groupBy {
	case "endpoint", "rule_version", "attribution_reason", "expected_schedule_state":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if !isVehicleType(*episodeType) {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	case "static_found":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if *episodeType != episodeTypeUnmatchedStop && *episodeType != episodeTypeUnmatchedTrip {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	case "route_id":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if *episodeType != episodeTypeUnmatchedTrip && !isVehicleType(*episodeType) {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	case "cluster":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if *episodeType != episodeTypeUnmatchedStop {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	case "bounding_box_version":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if *episodeType != episodeTypeStoppedOutOfBounds {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	case "position_validation_reason":
		if episodeType == nil {
			bad.add("group_by", "requires episode_type")
			return
		}
		if *episodeType != episodeTypeInvalidCoordinate {
			bad.add("group_by", "does not apply to episode_type "+*episodeType)
		}
	}
}

// buildEpisodeCountsItems builds the ungrouped items.
func buildEpisodeCountsItems(
	stopRows []dbgen.CountUnmatchedStopEpisodesRow,
	tripRows []dbgen.CountUnmatchedTripEpisodesRow,
	vehicleRows []dbgen.CountVehicleAnomalyEpisodesRow,
	filterType *string,
) []episodeCountItem {
	stopItem := episodeCountItem{
		EpisodeType: episodeTypeUnmatchedStop,
		Closed:      initialCloseReasons(unmatchedCloseReasons),
	}
	for _, r := range stopRows {
		stopItem.Opened = r.Opened
		stopItem.OpenAtTo = r.OpenAtTo
		if r.CloseReason != nil {
			stopItem.Closed[*r.CloseReason] = r.Closed
		}
	}

	tripItem := episodeCountItem{
		EpisodeType: episodeTypeUnmatchedTrip,
		Closed:      initialCloseReasons(unmatchedCloseReasons),
	}
	for _, r := range tripRows {
		tripItem.Opened = r.Opened
		tripItem.OpenAtTo = r.OpenAtTo
		if r.CloseReason != nil {
			tripItem.Closed[*r.CloseReason] = r.Closed
		}
	}

	vehicleItems := map[string]*episodeCountItem{
		episodeTypeStaleSource: {
			EpisodeType: episodeTypeStaleSource,
			Closed:      initialCloseReasons(vehicleCloseReasons),
		},
		episodeTypeInvalidCoordinate: {
			EpisodeType: episodeTypeInvalidCoordinate,
			Closed:      initialCloseReasons(vehicleCloseReasons),
		},
		episodeTypeStoppedOutOfBounds: {
			EpisodeType: episodeTypeStoppedOutOfBounds,
			Closed:      initialCloseReasons(vehicleCloseReasons),
		},
	}

	for _, r := range vehicleRows {
		if it, ok := vehicleItems[r.AnomalyCode]; ok {
			it.Opened = r.Opened
			it.OpenAtTo = r.OpenAtTo
			if r.CloseReason != nil {
				it.Closed[*r.CloseReason] = r.Closed
			}
		}
	}

	all := []episodeCountItem{
		stopItem,
		tripItem,
		*vehicleItems[episodeTypeStaleSource],
		*vehicleItems[episodeTypeInvalidCoordinate],
		*vehicleItems[episodeTypeStoppedOutOfBounds],
	}

	if filterType != nil {
		for _, it := range all {
			if it.EpisodeType == *filterType {
				return []episodeCountItem{it}
			}
		}
	}
	return all
}

type clusterValue struct {
	CellID        string  `json:"cell_id"`
	RootStationID *string `json:"root_station_id"`
}

type stopEntityValue struct {
	ReportedStopID int64  `json:"reported_stop_id"`
	StopID         string `json:"stop_id"`
}

type internalStopEntity struct {
	ReportedStopID int64  `json:"reported_stop_id"`
	StopID         string `json:"stop_id"`
	AgencyID       int64  `json:"agency_id"`
}

type tripEntityValue struct {
	ReportedTripID int64  `json:"reported_trip_id"`
	TripID         string `json:"trip_id"`
}

type internalTripEntity struct {
	ReportedTripID int64  `json:"reported_trip_id"`
	TripID         string `json:"trip_id"`
	AgencyID       int64  `json:"agency_id"`
}

type vehicleEntityValue struct {
	VehicleEntityID int64  `json:"vehicle_entity_id"`
	VehicleID       string `json:"vehicle_id"`
}

type internalVehicleEntity struct {
	VehicleEntityID int64  `json:"vehicle_entity_id"`
	VehicleID       string `json:"vehicle_id"`
	EndpointID      int64  `json:"endpoint_id"`
}

type parsedGroupKey struct {
	value              any
	internalAgencyID   *int64
	internalEndpointID *int64
	hasStopID          *string
	hasTripID          *string
	hasVehicleID       *string
	reportedStopID     *int64
	reportedTripID     *int64
	vehicleEntityID    *int64
}

func parseGroupKey(dimension string, key *string) (parsedGroupKey, error) {
	if key == nil {
		return parsedGroupKey{value: nil}, nil
	}
	str := *key
	switch dimension {
	case "agency", "endpoint", "opening_response", "closing_response", "gtfs_static_version", "rule_version", "bounding_box_version":
		id, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			return parsedGroupKey{}, err
		}
		return parsedGroupKey{value: id}, nil
	case "static_found":
		return parsedGroupKey{value: str == "true"}, nil
	case "route_id", "position_validation_reason", "attribution_reason", "expected_schedule_state", "recurrence", "opened_hour", "opened_day":
		return parsedGroupKey{value: str}, nil
	case "cluster":
		var cv clusterValue
		if err := json.Unmarshal([]byte(str), &cv); err != nil {
			return parsedGroupKey{}, err
		}
		return parsedGroupKey{value: cv}, nil
	case "entity":
		var stopEnt internalStopEntity
		if err := json.Unmarshal([]byte(str), &stopEnt); err == nil && stopEnt.ReportedStopID != 0 {
			val := stopEntityValue{ReportedStopID: stopEnt.ReportedStopID, StopID: stopEnt.StopID}
			return parsedGroupKey{
				value:            val,
				internalAgencyID: &stopEnt.AgencyID,
				hasStopID:        &stopEnt.StopID,
				reportedStopID:   &stopEnt.ReportedStopID,
			}, nil
		}
		var tripEnt internalTripEntity
		if err := json.Unmarshal([]byte(str), &tripEnt); err == nil && tripEnt.ReportedTripID != 0 {
			val := tripEntityValue{ReportedTripID: tripEnt.ReportedTripID, TripID: tripEnt.TripID}
			return parsedGroupKey{
				value:            val,
				internalAgencyID: &tripEnt.AgencyID,
				hasTripID:        &tripEnt.TripID,
				reportedTripID:   &tripEnt.ReportedTripID,
			}, nil
		}
		var vehEnt internalVehicleEntity
		if err := json.Unmarshal([]byte(str), &vehEnt); err == nil && vehEnt.VehicleEntityID != 0 {
			val := vehicleEntityValue{VehicleEntityID: vehEnt.VehicleEntityID, VehicleID: vehEnt.VehicleID}
			return parsedGroupKey{
				value:              val,
				internalEndpointID: &vehEnt.EndpointID,
				hasVehicleID:       &vehEnt.VehicleID,
				vehicleEntityID:    &vehEnt.VehicleEntityID,
			}, nil
		}
		return parsedGroupKey{}, fmt.Errorf("unknown entity format: %s", str)
	default:
		return parsedGroupKey{value: str}, nil
	}
}

func compareValues(a, b any) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch va := a.(type) {
	case int64:
		if vb, ok := b.(int64); ok {
			if va < vb {
				return -1
			} else if va > vb {
				return 1
			}
			return 0
		}
	case bool:
		if vb, ok := b.(bool); ok {
			if !va && vb {
				return -1
			} else if va && !vb {
				return 1
			}
			return 0
		}
	case string:
		if vb, ok := b.(string); ok {
			return strings.Compare(va, vb)
		}
	case clusterValue:
		if vb, ok := b.(clusterValue); ok {
			if c := strings.Compare(va.CellID, vb.CellID); c != 0 {
				return c
			}
			if va.RootStationID == nil && vb.RootStationID == nil {
				return 0
			}
			if va.RootStationID == nil {
				return -1
			}
			if vb.RootStationID == nil {
				return 1
			}
			return strings.Compare(*va.RootStationID, *vb.RootStationID)
		}
	case stopEntityValue:
		if vb, ok := b.(stopEntityValue); ok {
			if va.ReportedStopID != vb.ReportedStopID {
				if va.ReportedStopID < vb.ReportedStopID {
					return -1
				}
				return 1
			}
			return strings.Compare(va.StopID, vb.StopID)
		}
	case tripEntityValue:
		if vb, ok := b.(tripEntityValue); ok {
			if va.ReportedTripID != vb.ReportedTripID {
				if va.ReportedTripID < vb.ReportedTripID {
					return -1
				}
				return 1
			}
			return strings.Compare(va.TripID, vb.TripID)
		}
	case vehicleEntityValue:
		if vb, ok := b.(vehicleEntityValue); ok {
			if va.VehicleEntityID != vb.VehicleEntityID {
				if va.VehicleEntityID < vb.VehicleEntityID {
					return -1
				}
				return 1
			}
			return strings.Compare(va.VehicleID, vb.VehicleID)
		}
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return strings.Compare(string(ja), string(jb))
}

func buildEpisodesLink(
	epType string,
	dimension string,
	parsed parsedGroupKey,
	serverID *int64,
	agencyID *int64,
	from time.Time,
	to time.Time,
	schemeVersion *string,
) (string, bool) {
	vals := url.Values{}
	if serverID != nil {
		vals.Set("server_id", strconv.FormatInt(*serverID, 10))
	}
	if agencyID != nil && dimension != "agency" && dimension != "entity" {
		vals.Set("agency_id", strconv.FormatInt(*agencyID, 10))
	}

	timeFormat := func(t time.Time) string {
		return t.UTC().Format(time.RFC3339Nano)
	}

	if dimension == "opened_hour" {
		t, err := time.Parse(time.RFC3339, parsed.value.(string))
		if err == nil {
			vals.Set("opened_from", timeFormat(t))
			vals.Set("opened_to", timeFormat(t.Add(time.Hour)))
		}
	} else if dimension == "opened_day" {
		t, err := time.Parse(time.RFC3339, parsed.value.(string))
		if err == nil {
			vals.Set("opened_from", timeFormat(t))
			vals.Set("opened_to", timeFormat(t.Add(24*time.Hour)))
		}
	} else {
		vals.Set("opened_from", timeFormat(from))
		vals.Set("opened_to", timeFormat(to))
	}

	if isVehicleType(epType) {
		vals.Set("anomaly_type", epType)
	}

	switch dimension {
	case "agency":
		if parsed.value == nil {
			if isVehicleType(epType) {
				vals.Set("agency_id", "none")
			} else {
				return "", false
			}
		} else {
			vals.Set("agency_id", strconv.FormatInt(parsed.value.(int64), 10))
		}
	case "endpoint":
		vals.Set("endpoint_id", strconv.FormatInt(parsed.value.(int64), 10))
	case "opening_response":
		vals.Set("opening_response_id", strconv.FormatInt(parsed.value.(int64), 10))
	case "closing_response":
		if parsed.value == nil {
			vals.Set("status", "open")
		} else {
			vals.Set("closing_response_id", strconv.FormatInt(parsed.value.(int64), 10))
		}
	case "static_found":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("static_found", strconv.FormatBool(parsed.value.(bool)))
	case "gtfs_static_version":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("gtfs_static_version_id", strconv.FormatInt(parsed.value.(int64), 10))
	case "route_id":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("route_id", parsed.value.(string))
	case "cluster":
		if parsed.value == nil {
			return "", false
		}
		cv := parsed.value.(clusterValue)
		if schemeVersion != nil {
			vals.Set("scheme_version", *schemeVersion)
		}
		vals.Set("cell_id", cv.CellID)
		if cv.RootStationID != nil {
			vals.Set("root_station_id", *cv.RootStationID)
		}
	case "rule_version":
		vals.Set("rule_version_id", strconv.FormatInt(parsed.value.(int64), 10))
	case "bounding_box_version":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("bounding_box_version_id", strconv.FormatInt(parsed.value.(int64), 10))
	case "position_validation_reason":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("position_validation_reason", parsed.value.(string))
	case "attribution_reason":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("attribution_reason", parsed.value.(string))
	case "expected_schedule_state":
		if parsed.value == nil {
			return "", false
		}
		vals.Set("expected_schedule_state", parsed.value.(string))
	case "recurrence":
		vals.Set("recurrence", parsed.value.(string))
	case "entity":
		if parsed.internalAgencyID != nil {
			vals.Set("agency_id", strconv.FormatInt(*parsed.internalAgencyID, 10))
		}
		if parsed.hasStopID != nil {
			vals.Set("stop_id", *parsed.hasStopID)
		}
		if parsed.hasTripID != nil {
			vals.Set("trip_id", *parsed.hasTripID)
		}
		if parsed.internalEndpointID != nil {
			vals.Set("endpoint_id", strconv.FormatInt(*parsed.internalEndpointID, 10))
		}
		if parsed.hasVehicleID != nil {
			vals.Set("vehicle_id", *parsed.hasVehicleID)
		}
	}

	var basePath string
	switch epType {
	case episodeTypeUnmatchedStop:
		basePath = "/v1/unmatched-stop-episodes"
	case episodeTypeUnmatchedTrip:
		basePath = "/v1/unmatched-trip-episodes"
	default:
		basePath = "/v1/vehicle-anomaly-episodes"
	}
	return linkWithQuery(basePath, vals), true
}

func buildResourceLink(dimension string, epType string, parsed parsedGroupKey) (string, *related) {
	if parsed.value == nil {
		return "", nil
	}
	switch dimension {
	case "agency":
		id := parsed.value.(int64)
		return "agency", &related{
			Kind: kindAgency,
			ID:   id,
			URL:  "/v1/agencies/" + strconv.FormatInt(id, 10),
		}
	case "endpoint":
		id := parsed.value.(int64)
		return "endpoint", &related{
			Kind: kindEndpoint,
			ID:   id,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(id, 10),
		}
	case "opening_response":
		id := parsed.value.(int64)
		if isVehicleType(epType) {
			return "opening_response", &related{
				Kind: "vehicle_response",
				ID:   id,
				URL:  "/v1/vehicle-position-responses/" + strconv.FormatInt(id, 10),
			}
		}
		return "opening_response", &related{
			Kind: kindOBAMetricsResponse,
			ID:   id,
			URL:  "/v1/oba-metrics-responses/" + strconv.FormatInt(id, 10),
		}
	case "closing_response":
		id := parsed.value.(int64)
		if isVehicleType(epType) {
			return "closing_response", &related{
				Kind: "vehicle_response",
				ID:   id,
				URL:  "/v1/vehicle-position-responses/" + strconv.FormatInt(id, 10),
			}
		}
		return "closing_response", &related{
			Kind: kindOBAMetricsResponse,
			ID:   id,
			URL:  "/v1/oba-metrics-responses/" + strconv.FormatInt(id, 10),
		}
	case "gtfs_static_version":
		id := parsed.value.(int64)
		return "gtfs_static_version", &related{
			Kind: "gtfs_static_version",
			ID:   id,
			URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(id, 10),
		}
	case "rule_version":
		id := parsed.value.(int64)
		return "rule_version", &related{
			Kind: kindVehicleAnomalyRuleVersion,
			ID:   id,
			URL:  "/v1/vehicle-anomaly-rule-versions/" + strconv.FormatInt(id, 10),
		}
	case "bounding_box_version":
		id := parsed.value.(int64)
		return "bounding_box_version", &related{
			Kind: "bounding_box_version",
			ID:   id,
			URL:  "/v1/bounding-box-versions/" + strconv.FormatInt(id, 10),
		}
	case "entity":
		if parsed.reportedStopID != nil {
			id := *parsed.reportedStopID
			return "reported_stop", &related{
				Kind: kindReportedStop,
				ID:   id,
				URL:  "/v1/reported-stops/" + strconv.FormatInt(id, 10),
			}
		}
		if parsed.reportedTripID != nil {
			id := *parsed.reportedTripID
			return "reported_trip", &related{
				Kind: kindReportedTrip,
				ID:   id,
				URL:  "/v1/reported-trips/" + strconv.FormatInt(id, 10),
			}
		}
		if parsed.vehicleEntityID != nil {
			id := *parsed.vehicleEntityID
			return "vehicle", &related{
				Kind: "vehicle",
				ID:   id,
				URL:  "/v1/vehicles/" + strconv.FormatInt(id, 10),
			}
		}
	}
	return "", nil
}

// getEpisodeCounts serves GET /v1/episode-counts (E41): summary counts of episodes
// opened, closed, and open at the end of a window.
func (s *InvestigationService) getEpisodeCounts(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "from", "to", "server_id", "agency_id", "episode_type", "group_by", "scheme_version", "include")
	serverID := paramID(values, "server_id", &bad)
	agencyID := paramID(values, "agency_id", &bad)
	episodeType := paramEnum(values, "episode_type", allowedEpisodeTypes, &bad)
	groupBy := paramEnum(values, "group_by", allowedGroupBys, &bad)
	schemeVersion := paramString(values, "scheme_version", &bad)

	win := s.parseWindow(values, countsWindowOptions(), &bad)
	if groupBy != nil && *groupBy == "opened_hour" && win.From != nil && win.To != nil {
		if win.To.Sub(*win.From) > 7*24*time.Hour {
			bad.add("to", "must be within 7 days of from")
		}
	}

	if groupBy != nil {
		validateDimensionType(*groupBy, episodeType, &bad)
	}

	inc := s.parseInclude(values, includableForGroupBy(groupBy, episodeType), &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	if groupBy == nil {
		// Ungrouped counts
		var (
			stopRows    []dbgen.CountUnmatchedStopEpisodesRow
			tripRows    []dbgen.CountUnmatchedTripEpisodesRow
			vehicleRows []dbgen.CountVehicleAnomalyEpisodesRow
		)
		err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
			var err error
			if episodeType == nil || *episodeType == episodeTypeUnmatchedStop {
				stopRows, err = q.CountUnmatchedStopEpisodes(ctx, dbgen.CountUnmatchedStopEpisodesParams{
					FromTime: *win.From,
					ToTime:   *win.To,
					ServerID: serverID,
					AgencyID: agencyID,
				})
				if err != nil {
					return err
				}
			}
			if episodeType == nil || *episodeType == episodeTypeUnmatchedTrip {
				tripRows, err = q.CountUnmatchedTripEpisodes(ctx, dbgen.CountUnmatchedTripEpisodesParams{
					FromTime: *win.From,
					ToTime:   *win.To,
					ServerID: serverID,
					AgencyID: agencyID,
				})
				if err != nil {
					return err
				}
			}
			if episodeType == nil || isVehicleType(*episodeType) {
				vehicleRows, err = q.CountVehicleAnomalyEpisodes(ctx, dbgen.CountVehicleAnomalyEpisodesParams{
					FromTime: *win.From,
					ToTime:   *win.To,
					ServerID: serverID,
					AgencyID: agencyID,
				})
			}
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}

		resp := episodeCountsResponse{
			From:        newAPITime(*win.From),
			To:          newAPITime(*win.To),
			ServerID:    serverID,
			AgencyID:    agencyID,
			EpisodeType: episodeType,
			Items:       buildEpisodeCountsItems(stopRows, tripRows, vehicleRows, episodeType),
			Links: links{
				"self": linkWithQuery(r.URL.Path, values),
			},
		}
		s.writeJSON(w, r, resp, cacheShort())
		return
	}

	// Grouped counts
	var resolvedScheme *string
	var schemeErr *invalidParam
	var allGroupItems []groupedEpisodeCountItem
	var allLinks []itemLinks
	var isTruncated bool

	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		if *groupBy == "cluster" {
			if schemeVersion != nil {
				_, err := q.GetClusterScheme(ctx, *schemeVersion)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						schemeErr = &invalidParam{Name: "scheme_version", Reason: "unknown cluster scheme"}
						return nil
					}
					return err
				}
				resolvedScheme = schemeVersion
			} else {
				latest, err := q.GetLatestClusterScheme(ctx)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						schemeErr = &invalidParam{Name: "scheme_version", Reason: "unknown cluster scheme"}
						return nil
					}
					return err
				}
				resolvedScheme = &latest.SchemeVersion
			}
		}

		type groupAccumulator struct {
			epType   string
			key      *string
			opened   int64
			openAtTo int64
			closed   map[string]int64
		}
		groupMap := make(map[string]*groupAccumulator)
		groupOrder := make([]string, 0)

		getAcc := func(epType string, key *string) *groupAccumulator {
			mapKey := epType + "|"
			if key != nil {
				mapKey += "v" + *key
			} else {
				mapKey += "n"
			}
			acc, ok := groupMap[mapKey]
			if !ok {
				var closeReasons []string
				if isVehicleType(epType) {
					closeReasons = vehicleCloseReasons
				} else {
					closeReasons = unmatchedCloseReasons
				}
				acc = &groupAccumulator{
					epType: epType,
					key:    key,
					closed: initialCloseReasons(closeReasons),
				}
				groupMap[mapKey] = acc
				groupOrder = append(groupOrder, mapKey)
			}
			return acc
		}

		// 1. Unmatched stops
		if episodeType == nil || *episodeType == episodeTypeUnmatchedStop {
			if *groupBy != "endpoint" && *groupBy != "rule_version" && *groupBy != "bounding_box_version" &&
				*groupBy != "position_validation_reason" && *groupBy != "attribution_reason" &&
				*groupBy != "expected_schedule_state" && *groupBy != "route_id" {
				rows, err := q.CountUnmatchedStopEpisodesGrouped(ctx, dbgen.CountUnmatchedStopEpisodesGroupedParams{
					Dimension:     *groupBy,
					SchemeVersion: resolvedScheme,
					ServerID:      serverID,
					AgencyID:      agencyID,
					ToTime:        *win.To,
					FromTime:      *win.From,
				})
				if err != nil {
					return err
				}
				for _, r := range rows {
					acc := getAcc(episodeTypeUnmatchedStop, r.GroupKey)
					acc.opened = r.Opened
					acc.openAtTo = r.OpenAtTo
					if r.CloseReason != nil {
						acc.closed[*r.CloseReason] = r.Closed
					}
				}
			}
		}

		// 2. Unmatched trips
		if episodeType == nil || *episodeType == episodeTypeUnmatchedTrip {
			if *groupBy != "endpoint" && *groupBy != "rule_version" && *groupBy != "bounding_box_version" &&
				*groupBy != "position_validation_reason" && *groupBy != "attribution_reason" &&
				*groupBy != "expected_schedule_state" && *groupBy != "cluster" {
				rows, err := q.CountUnmatchedTripEpisodesGrouped(ctx, dbgen.CountUnmatchedTripEpisodesGroupedParams{
					Dimension: *groupBy,
					ServerID:  serverID,
					AgencyID:  agencyID,
					ToTime:    *win.To,
					FromTime:  *win.From,
				})
				if err != nil {
					return err
				}
				for _, r := range rows {
					acc := getAcc(episodeTypeUnmatchedTrip, r.GroupKey)
					acc.opened = r.Opened
					acc.openAtTo = r.OpenAtTo
					if r.CloseReason != nil {
						acc.closed[*r.CloseReason] = r.Closed
					}
				}
			}
		}

		// 3. Vehicle anomalies
		if episodeType == nil || isVehicleType(*episodeType) {
			if *groupBy != "cluster" && *groupBy != "static_found" {
				var anomalyCode *string
				if episodeType != nil {
					anomalyCode = episodeType
				}
				rows, err := q.CountVehicleAnomalyEpisodesGrouped(ctx, dbgen.CountVehicleAnomalyEpisodesGroupedParams{
					Dimension:   *groupBy,
					ServerID:    serverID,
					AgencyID:    agencyID,
					AnomalyCode: anomalyCode,
					ToTime:      *win.To,
					FromTime:    *win.From,
				})
				if err != nil {
					return err
				}
				for _, r := range rows {
					acc := getAcc(r.AnomalyCode, r.GroupKey)
					acc.opened = r.Opened
					acc.openAtTo = r.OpenAtTo
					if r.CloseReason != nil {
						acc.closed[*r.CloseReason] = r.Closed
					}
				}
			}
		}

		type groupedEntry struct {
			item  groupedEpisodeCountItem
			links itemLinks
		}
		var entries []groupedEntry

		for _, mk := range groupOrder {
			acc := groupMap[mk]
			parsed, err := parseGroupKey(*groupBy, acc.key)
			if err != nil {
				return err
			}

			itemL := itemLinks{}
			epLink, hasEpLink := buildEpisodesLink(acc.epType, *groupBy, parsed, serverID, agencyID, *win.From, *win.To, resolvedScheme)
			resName, resRel := buildResourceLink(*groupBy, acc.epType, parsed)
			if resRel != nil {
				itemL[resName] = *resRel
			}

			itLinks := links{}
			if hasEpLink {
				itLinks["episodes"] = epLink
			}
			if resRel != nil {
				itLinks[resName] = resRel.URL
			}

			entries = append(entries, groupedEntry{
				item: groupedEpisodeCountItem{
					EpisodeType: acc.epType,
					Value:       parsed.value,
					Opened:      acc.opened,
					Closed:      acc.closed,
					OpenAtTo:    acc.openAtTo,
					Links:       itLinks,
				},
				links: itemL,
			})
		}

		isTime := *groupBy == "opened_hour" || *groupBy == "opened_day"
		slices.SortFunc(entries, func(a, b groupedEntry) int {
			if isTime {
				ta := a.item.Value.(string)
				tb := b.item.Value.(string)
				if c := strings.Compare(ta, tb); c != 0 {
					return c
				}
				return strings.Compare(a.item.EpisodeType, b.item.EpisodeType)
			}
			if a.item.Opened != b.item.Opened {
				if a.item.Opened > b.item.Opened {
					return -1
				}
				return 1
			}
			if a.item.EpisodeType != b.item.EpisodeType {
				return strings.Compare(a.item.EpisodeType, b.item.EpisodeType)
			}
			return compareValues(a.item.Value, b.item.Value)
		})

		if len(entries) > 200 {
			isTruncated = true
			entries = entries[:200]
		}

		allGroupItems = make([]groupedEpisodeCountItem, len(entries))
		allLinks = make([]itemLinks, len(entries))
		for i, e := range entries {
			allGroupItems[i] = e.item
			allLinks[i] = e.links
		}

		return nil
	})

	if schemeErr != nil {
		writeInvalidParams(w, invalidParams{*schemeErr})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var incRes included
	if inc.requested() {
		err = s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
			var err error
			incRes, err = s.loadIncluded(ctx, q, allLinks, inc)
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}

	resp := episodeCountsResponse{
		From:            newAPITime(*win.From),
		To:              newAPITime(*win.To),
		ServerID:        serverID,
		AgencyID:        agencyID,
		EpisodeType:     episodeType,
		GroupBy:         groupBy,
		Items:           allGroupItems,
		GroupsTruncated: &isTruncated,
		Included:        incRes,
		Links: links{
			"self": linkWithQuery(r.URL.Path, values),
		},
	}
	s.writeJSON(w, r, resp, cacheShort())
}
