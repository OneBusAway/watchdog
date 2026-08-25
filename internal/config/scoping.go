package config

import (
	"strings"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// Scope describes how a single ObaServer entry should be monitored.
//
// AgencyScope: the entry has an agency_id; monitor only that agency. Today’s
// behavior.
//
// ServerScope: the entry has no agency_id; Watchdog must enumerate the
// agencies the server serves (by combining /api/where/metrics.json with the
// static GTFS feeds the operator configured) and run the per-agency pipeline
// for each one.
//
// ServerMeta is shared by both: it carries the fields every per-agency metric
// needs as labels or inputs (server_name, server_url, agency_name, etc.).
type Scope interface{ isScope() }

// AgencyScope is the result for a server entry that names one agency.
type AgencyScope struct {
	ServerMeta ServerMeta
	AgencyID   string
	AgencyName string
}

func (AgencyScope) isScope() {}

// ServerScope is the result for a server entry that does not name an agency.
// StaticAgencies is the set of agency identities derived from the configured
// static feeds (one entry per agency declared in agency.txt). Each is a
// candidate for the per-agency pipeline; the metrics-collector cross-references
// them against /api/where/metrics.json at scrape time and runs the pipeline
// only for agencies OBA reports as live.
type ServerScope struct {
	ServerMeta     ServerMeta
	StaticAgencies []AgencyIdentity
}

func (ServerScope) isScope() {}

// AgencyIdentity is the minimum information we need to monitor one agency on a
// server: the agency_id (used for keying and metric labels) and the
// agency_name (used as a label).
type AgencyIdentity struct {
	AgencyID   string
	AgencyName string
}

// ServerMeta is the cross-scope metadata shared by every per-agency metric and
// every per-server probe. It's a thin wrapper around ObaServer so the
// metrics-collector doesn't have to read server-name/server-url fields off the
// original entry each time.
type ServerMeta struct {
	ServerName string
	ServerURL  string
	ApiKey     string
}

// MetaFrom builds a ServerMeta from an ObaServer entry. serverURL is the
// sanitized form of ObaBaseURL so it's stable across scrapes.
func MetaFrom(server models.ObaServer) ServerMeta {
	return ServerMeta{
		ServerName: server.ServerName,
		ServerURL:  utils.SanitizeServerURL(server.ObaBaseURL),
		ApiKey:     server.ObaApiKey,
	}
}

// ResolveScope turns a single ObaServer entry into either an AgencyScope or a
// ServerScope.
//
// Agency-mode is the easy case: if AgencyID is set, return AgencyScope.
// Server-mode requires inspecting the static store to discover which agencies
// the configured feeds declare; if no static bundles have been downloaded yet
// we return a ServerScope with an empty StaticAgencies list, which the
// metrics-collector treats as "no agencies to monitor this tick".
//
// Crucially, ResolveScope does NOT consult /api/where/metrics.json — that
// happens at scrape time in the metrics-collector. The cross-reference is
// done every scrape so newly-live agencies (or agencies that have gone away)
// are reflected immediately, while the static-bundle store updates only on
// the 24h refresh cycle.
func ResolveScope(server models.ObaServer, staticStore *gtfs.StaticStore, routeAgencyIndex *gtfs.RouteAgencyIndex) Scope {
	meta := MetaFrom(server)

	if strings.TrimSpace(server.AgencyID) != "" {
		return AgencyScope{
			ServerMeta: meta,
			AgencyID:   server.AgencyID,
			AgencyName: server.AgencyName,
		}
	}

	if staticStore == nil {
		return ServerScope{ServerMeta: meta}
	}

	agencies := discoverAgenciesForServer(server.ObaBaseURL, staticStore, routeAgencyIndex)
	return ServerScope{
		ServerMeta:     meta,
		StaticAgencies: agencies,
	}
}

// discoverAgenciesForServer inspects the static store for entries belonging to
// this server (oba_base_url prefix), and for each unique agency_id returns an
// AgencyIdentity carrying its id and (recovered from the route → agency index)
// agency name.
//
// Multi-agency static feeds are accepted: a single feed whose agency.txt
// declares agencies A and B contributes one serverKey per agency, so both
// A and B end up in the StaticAgencies list.
//
// routeAgencyIndex is consulted to recover the human-readable agency_name when
// the static store doesn't carry it directly (the bundle does, but going via
// the index keeps the look-up cheap). It may be nil during early startup.
//
// TODO(scoped-store): Today every agency on a server is resolved by reading
// staticStore under (oba_base_url, agency_id), and that key resolves to the
// SAME merged *StaticData held in staticStore — see storeStaticForServer in
// gtfs_bundles.go, which does `staticStore.Set(serverKey, mergedbundle)` for
// every declared agency. This is deliberate (memory stays O(bundles) via
// pointer sharing), but it loses the many-to-many relationship between
// agencies and configured feeds: we don't know which feeds declared which
// agency, so per-agency bbox and stop resolution can't be scoped to the
// agency's actual coverage.
//
// Vehicle attribution is NOT part of this any more: the RT vehicle pass
// resolves route_id -> agency_id through gtfs.RouteAgencyIndex, which is
// built per route rather than per feed, so per-agency vehicle metrics are
// already correct. What remains is geometry — see the bbox NOTE in
// storeStaticForServer.
//
// The fix: change the storage shape so each agency is scoped to the merged
// feed(s) associated with it, rather than every agency pointing to the same
// union. This will require stopping the merge step before per-agency
// storage (or keeping per-feed data alongside the merged bundle), and
// reintroducing a FeedURLs-equivalent field on AgencyIdentity so the
// resolver can hand the agency its feed list. We removed FeedURLs earlier
// because it was unwritten and unread; this TODO is the reason it will
// come back.
//
// See also the bbox NOTE + TODO in storeStaticForServer — both touch the
// same underlying storage-shape question.
func discoverAgenciesForServer(obaBaseURL string, staticStore *gtfs.StaticStore, routeAgencyIndex *gtfs.RouteAgencyIndex) []AgencyIdentity {
	prefix := utils.SanitizeServerURL(obaBaseURL) + "|"
	// agenciesByID indexes the agencies discovered under this server's
	// oba_base_url prefix by their agency_id. We use *AgencyIdentity values
	// (not values) so the two-pass fill below can update AgencyName in
	// place — first the ID is set when the agency is seen in the static
	// store, then the name is recovered from routeAgencyIndex if not
	// already populated.
	agenciesByID := make(map[string]*AgencyIdentity)

	staticStore.Range(func(serverKey string, _ *models.StaticData) bool {
		if !strings.HasPrefix(serverKey, prefix) {
			return true
		}
		agencyID := strings.TrimPrefix(serverKey, prefix)
		if agencyID == "" {
			return true
		}
		identity, ok := agenciesByID[agencyID]
		if !ok {
			identity = &AgencyIdentity{AgencyID: agencyID}
			agenciesByID[agencyID] = identity
		}
		// Recover agency_name from the route → agency index (the same map that
		// the static-download path populated). If the index is empty (e.g.,
		// the bundle hadn't been parsed yet), we leave agency_name blank; the
		// per-agency pipeline falls back to "" which is acceptable for label
		// cardinality.
		if identity.AgencyName == "" && routeAgencyIndex != nil {
			if name, ok := routeAgencyIndex.AgencyNameFor(obaBaseURL, agencyID); ok {
				identity.AgencyName = name
			}
		}
		return true
	})

	out := make([]AgencyIdentity, 0, len(agenciesByID))
	for _, id := range agenciesByID {
		out = append(out, *id)
	}
	return out
}

// ReportAgencyMissingStaticFeed is a small helper exported so the
// metrics-collector can produce a consistent Sentry warning when /metrics.json
// reports an agency for which we have no static bundle yet. Kept here so the
// tag set stays aligned with the rest of the config-layer reporting.
func ReportAgencyMissingStaticFeed(obaBaseURL, agencyID, serverName string) {
	report.ReportErrorWithSentryOptions(
		&missingStaticFeedError{obaBaseURL: obaBaseURL, agencyID: agencyID},
		report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   agencyID,
				"server_name": serverName,
			},
			ExtraContext: map[string]interface{}{
				"oba_base_url": obaBaseURL,
			},
			Level: sentry.LevelWarning,
		},
	)
}

// missingStaticFeedError wraps the "agency reported by /metrics.json has no
// static bundle" condition into a typed error so Sentry can group it.
type missingStaticFeedError struct {
	obaBaseURL string
	agencyID   string
}

func (e *missingStaticFeedError) Error() string {
	return "agency " + e.agencyID + " reported by /api/where/metrics.json for " + e.obaBaseURL + " but no static feed covers it"
}
