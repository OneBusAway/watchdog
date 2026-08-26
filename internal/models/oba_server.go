package models

import (
	"strings"

	"watchdog.onebusaway.org/internal/utils"
)

// serverKeySeparator divides the sanitized base URL from the agency ID in a
// server key. It lives here because this file owns both directions of the
// format: ServerKey composes, ParseServerKey and ServerKeyPrefix decompose.
// Nothing outside this file should spell the separator.
const serverKeySeparator = "|"

// ServerKey returns the unique identity of an OBA deployment: the sanitized
// base URL plus the agency ID. Agency IDs alone are only unique within a single
// OBA server, so distinct deployments can reuse values like "1" or "MTA"; the
// composite key keeps them from colliding in stores.
//
// It is expected to be called only with validated config values (oba_base_url
// is a required field), so the degenerate "" branch of SanitizeServerURL is not
// handled specially.
func ServerKey(baseURL, agencyID string) string {
	return ServerKeyPrefix(baseURL) + agencyID
}

// ServerKeyPrefix returns the portion of a server key shared by every agency on
// a deployment: the sanitized base URL plus the separator. Callers scanning a
// store for "everything belonging to this server" should match on this rather
// than reassembling the separator themselves.
func ServerKeyPrefix(baseURL string) string {
	return utils.SanitizeServerURL(baseURL) + serverKeySeparator
}

// ParseServerKey splits a server key back into its sanitized base URL and
// agency ID. ok is false for a string that is not a server key at all (no
// separator), which callers should treat as a corrupt entry rather than as an
// agency-less key — a genuine server-scoped key ends in the separator and
// parses with an empty agencyID and ok true.
func ParseServerKey(key string) (baseURL, agencyID string, ok bool) {
	return strings.Cut(key, serverKeySeparator)
}

// ObaServer represents a OneBusAway server configuration.
//
// Scoping: every entry carries a ServerName that identifies the OBA deployment.
// AgencyID and AgencyName are optional. When AgencyID is set the entry is
// scoped to that single agency (today's behavior); when AgencyID is absent the
// entry is server-scoped and Watchdog discovers the agencies it serves from
// /api/where/metrics.json cross-referenced with each static feed's agency.txt.
// In server-mode the Operator must list every static feed the server exposes;
// Watchdog refuses to guess agency ownership from a feed whose agency.txt is
// empty or ambiguous (it logs to Sentry and skips the feed's pipeline).
type ObaServer struct {
	ServerName      string       `json:"server_name"`
	AgencyName      string       `json:"agency_name,omitempty"`
	AgencyID        string       `json:"agency_id,omitempty"`
	ObaBaseURL      string       `json:"oba_base_url"`
	ObaApiKey       string       `json:"oba_api_key"`
	GtfsStaticFeeds []string     `json:"gtfs_static_feeds"`
	GtfsRTFeeds     []GtfsRTFeed `json:"gtfs_rt_feeds"`
}

// GtfsRTFeed is one GTFS-Realtime source. Trip updates are retained for future
// consumers; current monitoring uses vehicle positions.
type GtfsRTFeed struct {
	TripUpdateURL      string   `json:"trip_update_url"`
	VehiclePositionURL string   `json:"vehicle_position_url"`
	GtfsRTAPIKey       string   `json:"gtfs_rt_api_key"`
	GtfsRTAPIValue     string   `json:"gtfs_rt_api_value"`
	AgencyIDs          []string `json:"agency_ids"`
}

// NewObaServer creates a new ObaServer instance with the provided configuration.
func NewObaServer(serverName, agencyName, agencyID, baseURL, apiKey string, gtfsStaticFeeds []string, gtfsRTFeeds []GtfsRTFeed) *ObaServer {
	return &ObaServer{ServerName: serverName, AgencyName: agencyName, AgencyID: agencyID, ObaBaseURL: baseURL, ObaApiKey: apiKey, GtfsStaticFeeds: gtfsStaticFeeds, GtfsRTFeeds: gtfsRTFeeds}
}

// ServerKey returns the unique identity of this deployment (sanitized base URL
// plus agency ID). It delegates to the package-level ServerKey so the two can
// never diverge.
func (s ObaServer) ServerKey() string {
	return ServerKey(s.ObaBaseURL, s.AgencyID)
}

// IsServerScoped reports whether this entry monitors a whole OBA deployment
// rather than one named agency. It is the single most load-bearing distinction
// in the scoping design — a server-scoped entry discovers its agencies from
// agency.txt and owns every server key under its base URL, while an
// agency-scoped entry owns exactly one — so it is named here rather than
// re-spelled as an AgencyID emptiness check at each site.
func (s ObaServer) IsServerScoped() bool {
	return strings.TrimSpace(s.AgencyID) == ""
}

// OwnsServerKey reports whether the given store key belongs to this entry.
// This is the ownership rule the stores are pruned by: a server-scoped entry
// claims every key under its base URL, including agencies discovered from
// agency.txt that the configuration never names; an agency-scoped entry claims
// only its own key. Both kinds may share an oba_base_url.
func (s ObaServer) OwnsServerKey(key string) bool {
	if s.IsServerScoped() {
		return strings.HasPrefix(key, ServerKeyPrefix(s.ObaBaseURL))
	}
	return key == s.ServerKey()
}
