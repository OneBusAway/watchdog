package investigation

import (
	"context"
	"errors"
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

// unmatchedStopCluster is one group of unmatched-stop episodes open at a given
// time: those sharing a cell and root station (E38). The unassigned group has
// every cell and station field null and no episodes link.
type unmatchedStopCluster struct {
	CellID           *string  `json:"cell_id"`
	CellLevel        *int16   `json:"cell_level"`
	CenterLatitude   *float64 `json:"center_latitude"`
	CenterLongitude  *float64 `json:"center_longitude"`
	RootStationID    *string  `json:"root_station_id"`
	RootStationName  *string  `json:"root_station_name"`
	OpenEpisodeCount int64    `json:"open_episode_count"`
	Links            links    `json:"links"`
}

// unmatchedStopClustersBody is the E38 response. It is not paginated.
type unmatchedStopClustersBody struct {
	At            apiTime                `json:"at"`
	ServerID      int64                  `json:"server_id"`
	AgencyID      *int64                 `json:"agency_id"`
	SchemeVersion *string                `json:"scheme_version"`
	Items         []unmatchedStopCluster `json:"items"`
	Links         links                  `json:"links"`
}

// clusterKey identifies one assigned group. The station is a pointer-free pair
// so the key is comparable: hasStation is false when the assignment has no station.
type clusterKey struct {
	cellID     string
	stationID  string
	hasStation bool
}

// clusterGroup accumulates one group's episodes and the cell and station it came from.
type clusterGroup struct {
	key         clusterKey
	level       *int16
	latitude    *float64
	longitude   *float64
	stationName *string
	count       int64
}

// unmatchedStopClusterItems groups the episodes in rows, which are open at at,
// for the scheme schemeVersion (nil when no scheme exists). Assigned groups come
// first, by open_episode_count descending then cell_id ascending; the unassigned
// group is last. Episodes whose context or assignment is missing form the
// unassigned group.
func unmatchedStopClusterItems(serverID int64, agencyID *int64, at time.Time, schemeVersion *string, rows []dbgen.ListUnmatchedStopClusterMembersRow) []unmatchedStopCluster {
	groups := make(map[clusterKey]*clusterGroup)
	var unassigned int64
	for _, row := range rows {
		if row.CellID == nil {
			unassigned++
			continue
		}
		key := clusterKey{cellID: *row.CellID}
		if row.RootStationID != nil {
			key.stationID = *row.RootStationID
			key.hasStation = true
		}
		g, ok := groups[key]
		if !ok {
			g = &clusterGroup{
				key:         key,
				level:       row.CellLevel,
				latitude:    row.CenterLatitude,
				longitude:   row.CenterLongitude,
				stationName: row.RootStationName,
			}
			groups[key] = g
		}
		g.count++
	}

	ordered := make([]*clusterGroup, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	slices.SortFunc(ordered, func(a, b *clusterGroup) int {
		if a.count != b.count {
			if a.count > b.count {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.key.cellID, b.key.cellID); c != 0 {
			return c
		}
		if a.key.hasStation != b.key.hasStation {
			if !a.key.hasStation {
				return -1
			}
			return 1
		}
		return strings.Compare(a.key.stationID, b.key.stationID)
	})

	items := make([]unmatchedStopCluster, 0, len(ordered)+1)
	for _, g := range ordered {
		item := unmatchedStopCluster{
			CellID:           &g.key.cellID,
			CellLevel:        g.level,
			CenterLatitude:   g.latitude,
			CenterLongitude:  g.longitude,
			RootStationName:  g.stationName,
			OpenEpisodeCount: g.count,
			Links:            links{},
		}
		if g.key.hasStation {
			station := g.key.stationID
			item.RootStationID = &station
		}
		if schemeVersion != nil {
			item.Links["episodes"] = clusterEpisodesLink(serverID, agencyID, at, *schemeVersion, g.key)
		}
		items = append(items, item)
	}
	if unassigned > 0 {
		items = append(items, unmatchedStopCluster{
			OpenEpisodeCount: unassigned,
			Links:            links{},
		})
	}
	return items
}

// clusterEpisodesLink is the E38 episode-list link for one group. The parameters
// appear in the order the spec lists them, not sorted.
func clusterEpisodesLink(serverID int64, agencyID *int64, at time.Time, schemeVersion string, key clusterKey) string {
	pairs := [][2]string{{"server_id", strconv.FormatInt(serverID, 10)}}
	if agencyID != nil {
		pairs = append(pairs, [2]string{"agency_id", strconv.FormatInt(*agencyID, 10)})
	}
	pairs = append(pairs,
		[2]string{"open_at", at.UTC().Format(time.RFC3339Nano)},
		[2]string{"scheme_version", schemeVersion},
		[2]string{"cell_id", key.cellID},
	)
	if key.hasStation {
		pairs = append(pairs, [2]string{"root_station_id", key.stationID})
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, url.QueryEscape(p[0])+"="+url.QueryEscape(p[1]))
	}
	// Keep ':' and ',' readable in the timestamp, as linkWithQuery does.
	query := strings.NewReplacer("%3A", ":", "%2C", ",").Replace(strings.Join(parts, "&"))
	return "/v1/unmatched-stop-episodes?" + query
}

// listUnmatchedStopClusters serves GET /v1/unmatched-stop-clusters: the unmatched
// stops open at a given time, grouped by cell and root station (E38).
func (s *InvestigationService) listUnmatchedStopClusters(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "server_id", "at", "agency_id", "scheme_version")
	required(values, &bad, "server_id", "at")
	serverID := paramID(values, "server_id", &bad)
	at := paramTime(values, "at", &bad)
	agencyID := paramID(values, "agency_id", &bad)
	scheme := paramString(values, "scheme_version", &bad)
	notFuture("at", at, s.now(), &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var (
		schemeVersion *string
		rows          []dbgen.ListUnmatchedStopClusterMembersRow
		unknownScheme bool
	)
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		if scheme != nil {
			if _, err := q.GetClusterScheme(ctx, *scheme); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					unknownScheme = true
					return nil
				}
				return err
			}
			schemeVersion = scheme
		} else {
			// No scheme row at all leaves schemeVersion nil: every episode is unassigned.
			latest, err := q.GetLatestClusterScheme(ctx)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return err
			default:
				v := latest.SchemeVersion
				schemeVersion = &v
			}
		}
		var err error
		rows, err = q.ListUnmatchedStopClusterMembers(ctx, dbgen.ListUnmatchedStopClusterMembersParams{
			OpenAt:        *at,
			SchemeVersion: schemeVersion,
			ServerID:      *serverID,
			AgencyID:      agencyID,
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if unknownScheme {
		bad.add("scheme_version", "must name an existing cluster scheme")
		writeInvalidParams(w, bad)
		return
	}

	body := unmatchedStopClustersBody{
		At:            newAPITime(*at),
		ServerID:      *serverID,
		AgencyID:      agencyID,
		SchemeVersion: schemeVersion,
		Items:         unmatchedStopClusterItems(*serverID, agencyID, *at, schemeVersion, rows),
		Links:         links{"self": linkWithQuery(r.URL.Path, r.URL.Query())},
	}
	s.writeJSON(w, r, body, cacheShort())
}
