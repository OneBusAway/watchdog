package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// StartMetricsCollection begins a background goroutine that continuously
// collects metrics from all configured OBA servers at a regular interval.
//
// It uses a time.Ticker based on the FetchInterval from the configuration. The
// ticker triggers every FetchInterval seconds, allowing the application to
// periodically collect and update metrics related to OBA servers listed in
// the config.
//
// Server-scope behavior (added with the redesign):
//   - For each configured entry, ResolveScope returns either an AgencyScope
//     (one agency) or a ServerScope (potentially many agencies). The collector
//     fans out into the per-agency pipeline once per live agency.
//   - In server-mode, /api/where/metrics.json is probed every tick to discover
//     which configured agencies are currently served. Static-derived metrics
//     for unconfigured agencies are skipped (status-gauge flips to 0).
//
// The collection routine gracefully shuts down when the provided context is
// canceled, allowing the application to cleanly exit or restart.
//
// Purpose:
//   - Ensure consistent collection of operational, transit, and health-related
//     metrics.
//   - Drive metrics exposed on Prometheus endpoints, used in dashboards and
//     alerts.
//   - Monitor reliability and correctness of OBA and GTFS-RT server
//     integrations.
func (app *Application) StartMetricsCollection(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(app.ConfigService.Config.FetchInterval) * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				app.Logger.Info("Stopping metrics collection routine")
				return
			case <-ticker.C:
				for _, server := range app.ConfigService.Config.GetServers() {
					scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
					app.collectForScope(ctx, server, scope)
				}
			}
		}
	}()
}

// collectForScope dispatches a single configured ObaServer entry through the
// per-scope collection pipeline.
//
//   - AgencyScope: delegates to CollectMetricsForServer, which fetches this
//     entry's own GTFS-RT feed as part of the pipeline (today's behavior).
//   - ServerScope: probes /metrics.json, runs the agency-scoped checks once
//     for every agency that has a static bundle AND is reported by OBA, then
//     runs the GTFS-RT vehicle pass once for the whole server. Static-only
//     agencies (configured but not currently live) get
//     gtfs_static_agency_currently_live = 0 and are left out of both.
func (app *Application) collectForScope(ctx context.Context, server models.ObaServer, scope config.Scope) {
	switch s := scope.(type) {
	case config.AgencyScope:
		app.CollectMetricsForServer(ctx, server)
	case config.ServerScope:
		app.collectForServerScope(ctx, server, s)
	default:
		app.Logger.Error("Unknown scope type", "server_name", server.ServerName)
	}
}

// collectForServerScope runs the per-agency pipeline for every agency declared
// in the static store under this server's oba_base_url, gated on whether OBA
// currently reports the agency as live.
//
// Two things happen exactly once per tick, regardless of how many agencies
// the server serves: the RT feed is fetched (and stored under the
// server-scoped key), and the vehicle pass walks it. The per-agency loop
// covers only the checks that are genuinely per-agency. Walking the feed once
// per agency instead would multiply the VehicleReportCount counter by the
// agency count on every tick and file every vehicle under every agency's
// last-seen slot; attribution is the pass's job, via the route -> agency
// index.
func (app *Application) collectForServerScope(ctx context.Context, server models.ObaServer, scope config.ServerScope) {
	if len(scope.StaticAgencies) == 0 {
		app.Logger.Info("Server-scope entry has no static agencies yet; skipping tick", "server_name", server.ServerName)
		return
	}

	// Honour the backoff written below on ping failure. Without this check the
	// server-scope path would re-ping a dead server on every tick and the
	// backoff state stored under the agency-less serverKey would never be read.
	if nextRetryAt, exists := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey()); exists && time.Now().UTC().Before(nextRetryAt) {
		app.Logger.Info("Skipping server-scope collection due to backoff",
			"server_name", server.ServerName, "next_retry_at", nextRetryAt)
		return
	}

	// Server-ping once per server (the /current-time.json endpoint takes no
	// agency parameter; per the metrics relabeling in this redesign, the
	// ObaApiStatus gauge is server-scoped, not agency-scoped).
	ok := app.MetricsService.ServerPing(ctx, server)
	if !ok {
		app.ConfigService.BackoffStore.UpdateBackoff(server.ServerKey())
		app.Logger.Info("Skipping server-scope collection due to ping failure",
			"server_name", server.ServerName, "oba_base_url", server.ObaBaseURL)
		report.ReportErrorWithSentryOptions(
			fmt.Errorf("server ping failed for %s", server.ObaBaseURL),
			report.SentryReportOptions{
				Tags:         map[string]string{"server_name": server.ServerName},
				ExtraContext: map[string]interface{}{"oba_base_url": server.ObaBaseURL},
				Level:        sentry.LevelError,
			},
		)
		return
	}
	app.ConfigService.BackoffStore.ResetBackoff(server.ServerKey())

	// Probe /metrics.json for the live agency set.
	liveAgencies, err := app.probeLiveAgencies(ctx, server)
	if err != nil {
		// Treat as "no agencies live" for this tick; static bundles stay
		// stored so their introspection metrics keep emitting.
		app.Logger.Warn("Failed to probe /metrics.json; treating no agencies as live this tick",
			"server_name", server.ServerName, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  map[string]string{"server_name": server.ServerName},
			Level: sentry.LevelWarning,
		})
	}

	// Resolve the agencies that are currently live. These entries carry the
	// labels the vehicle pass emits, and the pass consults the route -> agency
	// index to decide which of them owns each vehicle in the merged feed.
	liveAgencyEntries := make([]models.ObaServer, 0, len(scope.StaticAgencies))
	// Every other metric labels server_url with the sanitized base URL, and the
	// dashboard's $server_url variable is sourced from those series. Use the
	// same form here so these gauges join with the rest (and so credentials
	// embedded in the configured URL never reach a label).
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	// The feed URLs do not vary by agency, so sanitize them once rather than
	// re-parsing every feed URL for every agency on the server.
	sanitizedFeeds := make([]string, len(server.GtfsStaticFeeds))
	for i, feedURL := range server.GtfsStaticFeeds {
		sanitizedFeeds[i] = utils.SanitizeServerURL(feedURL)
	}
	for _, agency := range scope.StaticAgencies {
		isLive := liveAgencies[agency.AgencyID]
		metrics.GtfsStaticAgencyCurrentlyLive.WithLabelValues(
			agency.AgencyID,
			agency.AgencyName,
			server.ServerName,
			serverURL,
		).Set(boolToFloat(isLive))

		// attribution_status for every configured feed URL on this server.
		for _, feedURL := range sanitizedFeeds {
			metrics.GtfsStaticFeedAttributionStatus.WithLabelValues(
				feedURL,
				agency.AgencyID,
				agency.AgencyName,
				server.ServerName,
				serverURL,
			).Set(boolToFloat(isLive))
		}

		if isLive {
			liveAgencyEntries = append(liveAgencyEntries, serverForAgency(server, agency.AgencyID, agency.AgencyName))
		}
	}

	// No live agency means there is nothing agency-scoped to check, nothing to
	// attribute a vehicle to, and no reason to spend an RT fetch. Returning
	// here rather than falling through also matters for correctness:
	// collectVehicleMetrics reads an empty agency slice as agency-mode, which
	// over a server-scoped entry would attribute every vehicle in the merged
	// feed to an empty agency_id.
	if len(liveAgencyEntries) == 0 {
		return
	}

	// Fetch the RT feed ONCE for the whole server, stored under the
	// server-scoped key (oba_base_url + empty agency). The vehicle pass reads
	// that one key and attributes each vehicle to its owning agency, so there
	// is no need to register the same feed under every agency's key. If the
	// fetch fails the pass still runs against whatever the previous tick left
	// in the store (nothing at all on the first tick); the error is reported to
	// Sentry once for visibility. This is a deliberate divergence from
	// agency-mode, where the same failure is a hard gate (see
	// CollectMetricsForServer): here the fetch is one step shared by every
	// agency on the server, so we surface it and keep going rather than drop
	// the whole server's vehicle pass, accepting that the pass may recompute
	// from a feed one or more ticks old.
	if err := app.GtfsService.FetchAndStoreGTFSRTFeed(ctx, server); err != nil {
		app.Logger.Error("Failed to fetch and store GTFS-RT feed",
			"server_name", server.ServerName, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	// Per-agency metric loop. Each iteration runs only the agency-scoped
	// checks (CheckBundleExpiration, FetchObaAPIMetrics,
	// CountActiveVehiclesForAgency, ServerPing) — the GTFS-RT vehicle pass
	// runs once for the whole server after this loop. ServerPing is
	// intentionally called once per agency here because the gauge is
	// server-scoped — the per-agency call is cheap (one HTTP probe) and keeps
	// the metric series up-to-date even if a single agency's checks bail out.
	//
	// TODO(server-mode dedup): In server-mode with N live agencies we
	// call FetchObaAPIMetrics once per agency, which means N redundant
	// HTTP fetches of the same /api/where/metrics.json endpoint per tick.
	// The response was already fetched once by probeLiveAgencies at the
	// top of this function.
	//
	// We accept this for now because the endpoint is small JSON designed
	// for high-QPS polling and at 30s ticks the cost is at most a few
	// redundant fetches/min. Threading the parsed body through
	// FetchObaAPIMetrics / MetricsService.FetchObaAPIMetrics / the
	// public CollectMetricsForServer path would require parameter
	// plumbing across three layers and a parsed-body cache for the
	// duration of one tick.
	//
	// Revisit if OBA starts rate-limiting /metrics.json, the fleet fans
	// out across many agencies, or scrape latency becomes a concern. The
	// fix is to widen probeLiveAgencies to return the parsed body and
	// pass it into FetchObaAPIMetrics (probably as an optional parameter
	// on MetricsService.FetchObaAPIMetrics so agency-mode callers don't
	// have to change). fetchObaAPIMetrics carries a one-line pointer to
	// this TODO at its definition site.
	for _, agencyServer := range liveAgencyEntries {
		app.collectAgencyChecks(ctx, agencyServer)
	}

	// One GTFS-RT vehicle pass for the whole server. Running it inside the
	// loop above would re-walk the same merged feed once per agency, which
	// inflates the VehicleReportCount counter by the agency count on every
	// tick and files every vehicle under every agency's last-seen slot.
	app.collectVehicleMetrics(server, liveAgencyEntries)
}

// CollectMetricsForServer performs all metric collection and validation logic
// for a single OBA server / agency, including fetching that entry's GTFS-RT
// feed. This is the agency-mode entry point.
//
// Ordering constraints:
//   - Backoff is non-blocking and lives in BackoffStore — not a time.Sleep.
//     Before each cycle collectAgencyChecks tests NextRetryAt; if a server is
//     still backing off, its entire collection is skipped this tick.
//   - The GTFS-RT feed must be in the realtime store before the vehicle pass
//     runs, so a failed fetch is a hard gate: we return rather than emit
//     metrics derived from a stale (or absent) feed.
func (app *Application) CollectMetricsForServer(ctx context.Context, server models.ObaServer) {
	if !app.collectAgencyChecks(ctx, server) {
		return
	}

	if err := app.GtfsService.FetchAndStoreGTFSRTFeed(ctx, server); err != nil {
		app.Logger.Error("Failed to fetch and store GTFS-RT feed", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"agency_name": server.AgencyName,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
		return
	}

	// nil agencies: this entry names a single agency, so every vehicle in the
	// feed belongs to it and the route -> agency index is not consulted.
	app.collectVehicleMetrics(server, nil)
}

// collectAgencyChecks runs the probes and validations that are scoped to a
// single agency and do NOT read the realtime store: backoff, server ping,
// bundle expiration, the OBA REST API metrics, and the vehicles-for-agency
// count.
//
// It returns false when collection for this entry should stop for the tick —
// either the entry is still backing off, or its ping failed (which grows the
// backoff). Every other step logs-and-continues, because one failing probe
// should not suppress the others.
//
// Server-mode calls this once per live agency; agency-mode calls it once for
// the configured entry. The GTFS-RT vehicle pass is deliberately NOT part of
// it — that pass runs once per server, in the caller.
func (app *Application) collectAgencyChecks(ctx context.Context, server models.ObaServer) bool {
	// Check if server has an active backoff period
	nextRetryAt, exists := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey())
	if exists && time.Now().UTC().Before(nextRetryAt) {
		app.Logger.Info("Skipping metrics collection due to backoff",
			"agency_id", server.AgencyID, "server_name", server.ServerName, "next_retry_at", nextRetryAt)
		report.ReportErrorWithSentryOptions(fmt.Errorf("skipping metrics collection for server %s due to backoff", server.ObaBaseURL), report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"agency_name": server.AgencyName,
				"server_name": server.ServerName,
			},
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
			Level: sentry.LevelInfo,
		})
		return false
	}

	ok := app.MetricsService.ServerPing(ctx, server)
	if !ok {
		app.Logger.Error("Server ping failed",
			"agency_id", server.AgencyID, "agency_name", server.AgencyName, "server_name", server.ServerName)
		report.ReportErrorWithSentryOptions(fmt.Errorf("server ping failed for %s", server.ObaBaseURL), report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"agency_name": server.AgencyName,
				"server_name": server.ServerName,
			},
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
			Level: sentry.LevelError,
		})
		app.ConfigService.BackoffStore.UpdateBackoff(server.ServerKey())
		app.Logger.Info("Skipping further metrics collection for server due to ping failure")
		return false
	}

	app.Logger.Info("Server ping successful",
		"agency_id", server.AgencyID, "agency_name", server.AgencyName, "server_name", server.ServerName)
	app.ConfigService.BackoffStore.ResetBackoff(server.ServerKey())

	_, _, err := app.MetricsService.CheckBundleExpiration(time.Now().UTC(), server)
	if err != nil {
		app.Logger.Error("Failed to check GTFS bundle expiration", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"agency_name": server.AgencyName,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	err = app.MetricsService.FetchObaAPIMetrics(ctx, server.AgencyID, server.AgencyName, server.ServerName, server.ObaBaseURL, server.ObaApiKey)
	if err != nil {
		app.Logger.Error("Failed to fetch OBA API metrics", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"agency_name": server.AgencyName,
				"server_name": server.ServerName,
			},
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
			Level: sentry.LevelError,
		})
	}

	err = app.MetricsService.CountActiveVehiclesForAgency(ctx, server)
	if err != nil {
		app.Logger.Error("Failed to count vehicles from VehiclesForAgency API", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	return true
}

// collectVehicleMetrics runs the three metric functions that read from the
// GTFS-RT realtime store. It walks the feed once and must therefore be called
// exactly once per server per tick.
//
// agencies is nil for an agency-scoped entry (every vehicle belongs to the
// configured agency) and holds the live agency entries for a server-scoped one
// (each vehicle is attributed through the route -> agency index). See the
// scope-dispatch comment at the top of internal/metrics/vehicle_metrics.go.
func (app *Application) collectVehicleMetrics(server models.ObaServer, agencies []models.ObaServer) {
	if err := app.MetricsService.CountVehiclePositions(server, agencies); err != nil {
		app.Logger.Error("Failed to count vehicle positions from GTFS-RT", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	if err := app.MetricsService.TrackVehicleTelemetry(server, agencies); err != nil {
		app.Logger.Error("Failed to track vehicle reporting frequency", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	if err := app.MetricsService.TrackInvalidVehiclesAndStoppedOutOfBounds(server, agencies); err != nil {
		app.Logger.Error("Failed to count invalid vehicle coordinates", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}
}

// serverForAgency returns a copy of `server` with the given agency_id /
// agency_name populated. Used by collectForServerScope to materialize the
// per-agency entry the existing pipeline expects.
func serverForAgency(server models.ObaServer, agencyID, agencyName string) models.ObaServer {
	return models.ObaServer{
		ServerName:      server.ServerName,
		AgencyID:        agencyID,
		AgencyName:      agencyName,
		ObaBaseURL:      server.ObaBaseURL,
		ObaApiKey:       server.ObaApiKey,
		GtfsStaticFeeds: server.GtfsStaticFeeds,
		GtfsRTFeeds:     server.GtfsRTFeeds,
	}
}

// boolToFloat converts a bool to the float64 Prometheus expects for gauge
// values (1.0 for true, 0.0 for false).
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// probeLiveAgencies fetches /api/where/metrics.json for the given server and
// returns the set of agency IDs OBA currently reports. Returns an empty map
// (not an error) if the response is missing or malformed — the caller treats
// empty as "no agencies live this tick" so static-only metrics keep emitting.
func (app *Application) probeLiveAgencies(ctx context.Context, server models.ObaServer) (map[string]bool, error) {
	endpoint := fmt.Sprintf("%s/api/where/metrics.json?key=%s", server.ObaBaseURL, url.QueryEscape(server.ObaApiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build /metrics.json request: %w", err)
	}

	client := app.MetricsService.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch /metrics.json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/metrics.json returned %d", resp.StatusCode)
	}
	// Reuse the metrics package's response type so the two decoders of this
	// endpoint can never drift apart. Only entry.AgencyIDs is read here.
	var decoded metrics.OBAMetrics
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode /metrics.json: %w", err)
	}

	out := make(map[string]bool, len(decoded.Data.Entry.AgencyIDs))
	for _, id := range decoded.Data.Entry.AgencyIDs {
		out[id] = true
	}
	return out, nil
}
