package investigation

import (
	"context"
	"net/http"

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

// episodeCountItem is the counts for one episode type (E41).
type episodeCountItem struct {
	EpisodeType string           `json:"episode_type"`
	Opened      int64            `json:"opened"`
	Closed      map[string]int64 `json:"closed"`
	OpenAtTo    int64            `json:"open_at_to"`
}

// episodeCountsResponse is the E41 response body.
type episodeCountsResponse struct {
	From     apiTime            `json:"from"`
	To       apiTime            `json:"to"`
	ServerID *int64             `json:"server_id"`
	AgencyID *int64             `json:"agency_id"`
	Items    []episodeCountItem `json:"items"`
	Links    links              `json:"links"`
}

// initialCloseReasons returns a map of allowed close reasons initialized to 0.
func initialCloseReasons(reasons []string) map[string]int64 {
	m := make(map[string]int64, len(reasons))
	for _, r := range reasons {
		m[r] = 0
	}
	return m
}

// buildEpisodeCountsItems builds the exact 5 items in required order from query rows.
func buildEpisodeCountsItems(
	stopRows []dbgen.CountUnmatchedStopEpisodesRow,
	tripRows []dbgen.CountUnmatchedTripEpisodesRow,
	vehicleRows []dbgen.CountVehicleAnomalyEpisodesRow,
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

	return []episodeCountItem{
		stopItem,
		tripItem,
		*vehicleItems[episodeTypeStaleSource],
		*vehicleItems[episodeTypeInvalidCoordinate],
		*vehicleItems[episodeTypeStoppedOutOfBounds],
	}
}

// getEpisodeCounts serves GET /v1/episode-counts (E41): summary counts of episodes
// opened, closed, and open at the end of a window.
func (s *InvestigationService) getEpisodeCounts(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "from", "to", "server_id", "agency_id")
	serverID := paramID(values, "server_id", &bad)
	agencyID := paramID(values, "agency_id", &bad)
	win := s.parseWindow(values, countsWindowOptions(), &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var (
		stopRows    []dbgen.CountUnmatchedStopEpisodesRow
		tripRows    []dbgen.CountUnmatchedTripEpisodesRow
		vehicleRows []dbgen.CountVehicleAnomalyEpisodesRow
	)
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		stopRows, err = q.CountUnmatchedStopEpisodes(ctx, dbgen.CountUnmatchedStopEpisodesParams{
			FromTime: *win.From,
			ToTime:   *win.To,
			ServerID: serverID,
			AgencyID: agencyID,
		})
		if err != nil {
			return err
		}

		tripRows, err = q.CountUnmatchedTripEpisodes(ctx, dbgen.CountUnmatchedTripEpisodesParams{
			FromTime: *win.From,
			ToTime:   *win.To,
			ServerID: serverID,
			AgencyID: agencyID,
		})
		if err != nil {
			return err
		}

		vehicleRows, err = q.CountVehicleAnomalyEpisodes(ctx, dbgen.CountVehicleAnomalyEpisodesParams{
			FromTime: *win.From,
			ToTime:   *win.To,
			ServerID: serverID,
			AgencyID: agencyID,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	resp := episodeCountsResponse{
		From:     newAPITime(*win.From),
		To:       newAPITime(*win.To),
		ServerID: serverID,
		AgencyID: agencyID,
		Items:    buildEpisodeCountsItems(stopRows, tripRows, vehicleRows),
		Links: links{
			"self": linkWithQuery(r.URL.Path, values),
		},
	}
	s.writeJSON(w, r, resp, cacheShort())
}
