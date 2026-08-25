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
//   - ServerScope: probes /metrics.json, then runs the per-agency pipeline
//     once for every agency that has a static bundle AND is reported by
//     OBA. Static-only agencies (configured but not currently live) get
//     gtfs_static_agency_currently_live = 0; no per-agency RT pipeline runs.
func (app *Application) collectForScope(ctx context.Context, server models.ObaServer, scope config.Scope) {
	switch s := scope.(type) {
	case config.AgencyScope:
		app.CollectMetricsForServer(server)
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
// The RT feed is fetched exactly once per tick — before the agency loop —
// and the same parsed *RealtimeData is registered under every agency's
// serverKey via FetchAndStoreGTFSRTFeedOnce. The per-agency loop then runs
// only the post-RT metric functions (collectVehicleMetrics) inside the
// shared CollectMetricsForServer path. For an N-agency server this is
// O(1) RT fetches instead of O(N).
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
	ok := app.MetricsService.ServerPing(server)
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

	// Resolve the set of agency serverKeys that are currently live. The RT
	// feed (fetched once below) is registered under every one of these keys,
	// pointer-shared, so each agency's per-tick pipeline sees the same
	// vehicle set without redundant HTTP fetches.
	liveAgencyKeys := make([]string, 0, len(scope.StaticAgencies))
	// Every other metric labels server_url with the sanitized base URL, and the
	// dashboard's $server_url variable is sourced from those series. Use the
	// same form here so these gauges join with the rest (and so credentials
	// embedded in the configured URL never reach a label).
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	for _, agency := range scope.StaticAgencies {
		isLive := liveAgencies[agency.AgencyID]
		metrics.GtfsStaticAgencyCurrentlyLive.WithLabelValues(
			agency.AgencyID,
			agency.AgencyName,
			server.ServerName,
			serverURL,
		).Set(boolToFloat(isLive))

		// attribution_status for every configured feed URL on this server.
		for _, feedURL := range server.GtfsStaticFeeds {
			metrics.GtfsStaticFeedAttributionStatus.WithLabelValues(
				utils.SanitizeServerURL(feedURL),
				agency.AgencyID,
				agency.AgencyName,
				server.ServerName,
				serverURL,
			).Set(boolToFloat(isLive))
		}

		if isLive {
			liveAgencyKeys = append(liveAgencyKeys, models.ServerKey(server.ObaBaseURL, agency.AgencyID))
		}
	}

	// Fetch the RT feed ONCE for the whole server and register the result
	// under every live agency's serverKey. If the fetch fails the per-agency
	// pipelines still run, but they will read whatever the previous tick left
	// in the realtime store (or nothing at all on the first tick). The error is
	// reported to Sentry once for visibility. When no agency is live we skip
	// the fetch entirely and no per-agency pipeline runs this tick.
	if len(liveAgencyKeys) > 0 {
		if err := app.GtfsService.FetchAndStoreGTFSRTFeedOnce(server, liveAgencyKeys); err != nil {
			app.Logger.Error("Failed to fetch and store GTFS-RT feed",
				"server_name", server.ServerName, "error", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"server_name": server.ServerName,
				},
				Level: sentry.LevelError,
			})
		}
	}

	// Per-agency metric loop. Each iteration runs the agency-scoped
	// pre-RT steps (CheckBundleExpiration, FetchObaAPIMetrics,
	// CountActiveVehiclesForAgency, ServerPing) and the post-RT vehicle
	// metrics via collectVehicleMetrics. ServerPing is intentionally
	// called once per agency here because the gauge is server-scoped —
	// the per-agency call is cheap (one HTTP probe) and keeps the metric
	// series up-to-date even if a single agency's pipeline bails out.
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
	for _, agency := range scope.StaticAgencies {
		if !liveAgencies[agency.AgencyID] {
			continue
		}
		agencyServer := serverForAgency(server, agency.AgencyID, agency.AgencyName)
		app.collectMetricsForServer(agencyServer, false)
	}
}

// CollectMetricsForServer performs all metric collection and validation logic
// for a single OBA server / agency, including fetching that entry's GTFS-RT
// feed. This is the agency-mode entry point.
func (app *Application) CollectMetricsForServer(server models.ObaServer) {
	app.collectMetricsForServer(server, true)
}

// collectMetricsForServer is the shared pipeline behind CollectMetricsForServer.
//
// It sequentially runs a series of probes and validations against the given
// server. Ordering constraints:
//   - Backoff is non-blocking and lives in BackoffStore — not a time.Sleep.
//     Before each cycle it checks NextRetryAt; if a server is still backing
//     off, its entire collection is skipped this tick.
//   - The GTFS-RT feed must be in the realtime store before the vehicle
//     metrics run. When fetchRealtime is true (agency-mode) this function
//     fetches it via GtfsService.FetchAndStoreGTFSRTFeed and returns early on
//     failure, because every check after that point reads the realtime store.
//     Server-mode passes false: collectForServerScope has already invoked
//     GtfsService.FetchAndStoreGTFSRTFeedOnce once for the whole server and
//     registered the result under every live agency's serverKey, so re-fetching
//     per agency would be N redundant HTTP calls per tick.
func (app *Application) collectMetricsForServer(server models.ObaServer, fetchRealtime bool) {
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
		return
	}

	ok := app.MetricsService.ServerPing(server)
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
		return
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

	err = app.MetricsService.FetchObaAPIMetrics(server.AgencyID, server.AgencyName, server.ServerName, server.ObaBaseURL, server.ObaApiKey)
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

	err = app.MetricsService.CountActiveVehiclesForAgency(server)
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

	// Fetch and store the GTFS-RT feed. Every check in collectVehicleMetrics
	// reads the realtime store, so a failure here is a hard gate: we return
	// rather than emit metrics derived from a stale (or absent) feed.
	if fetchRealtime {
		if err := app.GtfsService.FetchAndStoreGTFSRTFeed(server); err != nil {
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
	}

	app.collectVehicleMetrics(server)
}

// collectVehicleMetrics runs the three metric functions that read from the
// GTFS-RT realtime store. Extracted so server-mode can fetch the RT feed
// once per tick and call this for each agency without re-fetching.
func (app *Application) collectVehicleMetrics(server models.ObaServer) {
	if err := app.MetricsService.CountVehiclePositions(server); err != nil {
		app.Logger.Error("Failed to count vehicle positions from GTFS-RT", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	if err := app.MetricsService.TrackVehicleTelemetry(server); err != nil {
		app.Logger.Error("Failed to track vehicle reporting frequency", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
			Level: sentry.LevelError,
		})
	}

	if err := app.MetricsService.TrackInvalidVehiclesAndStoppedOutOfBounds(server); err != nil {
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
