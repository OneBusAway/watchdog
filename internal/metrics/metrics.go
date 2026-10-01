package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	WatchdogCollectionInterval = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "watchdog_collection_interval_seconds",
		Help: "Configured interval between Watchdog realtime collection cycles",
	})

	WatchdogCollectionLastCompleted = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "watchdog_collection_last_completed_timestamp_seconds",
		Help: "Unix timestamp when Watchdog last completed a full realtime collection cycle",
	})
)

// ObaApiStatus tracks the reachability of an OBA server's /current-time.json
// endpoint. The ping is server-wide (the endpoint takes no agency parameter),
// so the metric is labeled with server identity only — agency_id / agency_name
// labels were misleading from the start and have been dropped. server_url is
// the technical identifier (matches what's on every other per-agency metric);
// server_name is the human-friendly label that operators see in dashboards.
var ObaApiStatus = tracked(promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "oba_api_status",
		Help: "Status of the OneBusAway API Server (0 = not working, 1 = working)",
	},
	[]string{"server_name", "server_url"},
))

var (
	BundleEarliestExpirationGauge = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_earliest_expiration",
		Help: "Number of days until the earliest GTFS bundle expiration",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	BundleLatestExpirationGauge = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_latest_expiration",
		Help: "Number of days until the latest GTFS bundle expiration",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))
)

var (
	AgenciesTrackedCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "oba_tracked_agencies_count",
		Help: "Number of agencies currently tracked by Watchdog (validated config entries)",
	})

	AgenciesTrackedInfo = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_tracked_agencies_info",
		Help: "One series per agency currently tracked by Watchdog (always 1)",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))
)

var (
	GtfsRtLastSuccessfulFetch = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_last_successful_fetch_timestamp_seconds",
		Help: "Unix timestamp of the last successful fetch and parse of all configured GTFS-RT vehicle feeds for the server",
	}, []string{"server_name", "server_url"}))

	GtfsScheduleAvailable = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_schedule_available",
		Help: "Whether a complete schedule compiled from every configured static feed is currently available for confident evaluation",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	GtfsScheduledServiceActive = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_scheduled_service_active",
		Help: "Whether the complete static GTFS schedule has a trip window active now; meaningful only when gtfs_schedule_available is 1",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	GtfsStaticBundleCurrent = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_bundle_current",
		Help: "Whether the retained complete static bundle is usable, its latest refresh attempt succeeded, and its last success is no more than 26 hours old",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	GtfsStaticBundleUsable = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_bundle_usable",
		Help: "Whether a complete retained static bundle exists whose latest service end date still covers the agency-local date",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	RealtimeVehiclePositions = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "realtime_vehicle_positions_count_gtfs_rt",
		Help: "Number of realtime vehicle positions in the GTFS-RT feed",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	AgencyActiveVehiclesGauge = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agency_active_vehicles_count",
		Help: "Number of active vehicles reported for the agency by the OBA vehicles-for-agency API",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	GtfsRtVehicleSourceTimestamp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_vehicle_source_timestamp_seconds",
		Help: "Source-provided Unix timestamp for the current GTFS-RT vehicle observation",
	}, []string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"}))

	GtfsRtVehicleStateLastChangedTimestamp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_vehicle_state_last_changed_timestamp_seconds",
		Help: "Unix timestamp when Watchdog last observed semantic GTFS-RT vehicle content change, excluding the source timestamp",
	}, []string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"}))

	GtfsRtVehicleSourceTimestampAdvances = tracked(promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gtfs_rt_vehicle_source_timestamp_advances_total",
		Help: "Number of source timestamp advances across GTFS-RT vehicles",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed"}))

	GtfsRtVehicleStateChanges = tracked(promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gtfs_rt_vehicle_state_changes_total",
		Help: "Number of semantic GTFS-RT vehicle state changes, excluding source timestamp changes",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed"}))

	GtfsRtFeedFetchSuccess = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_fetch_success",
		Help: "Whether the latest GTFS-RT feed fetch and parse succeeded (1) or failed (0)",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	GtfsRtFeedLastSuccessfulFetchTimestamp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_last_successful_fetch_timestamp_seconds",
		Help: "Unix timestamp when Watchdog last successfully fetched and parsed this GTFS-RT feed",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	GtfsRtFeedSourceTimestamp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_source_timestamp_seconds",
		Help: "Source-provided GTFS-RT FeedHeader Unix timestamp, when available",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	GtfsRtFeedPayloadLastChangedTimestamp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_payload_last_changed_timestamp_seconds",
		Help: "Unix timestamp when Watchdog last observed a canonical feed payload change, excluding FeedHeader",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	GtfsRtFeedVehicleEntities = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_vehicle_entities",
		Help: "Number of vehicle-position entities in the latest successful GTFS-RT feed payload",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	GtfsRtFeedVehicleTimestampMissingCount = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_rt_feed_vehicle_timestamp_missing_count",
		Help: "Number of vehicle-position entities without a source timestamp in the latest successful feed payload",
	}, []string{"agency_id", "agency_name", "server_name", "server_url", "feed", "feed_url"}))

	VehicleSpeedGauge = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_vehicle_computed_speed",
			Help: "Computed speed of the vehicle in m/s based on GTFS-RT positions and timestamps",
		},
		[]string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"},
	))

	VehicleSpeedDiscrepancyRatioGauge = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_vehicle_speed_discrepancy_ratio",
			Help: "Ratio between computed and reported speed (|computed - reported| / reported)",
		},
		[]string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"},
	))

	GtfsRtVehicleSpeedLastComputedTimestamp = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_vehicle_speed_last_computed_timestamp_seconds",
			Help: "Unix timestamp when computed speed was last recalculated from two advancing valid GTFS-RT observations",
		},
		[]string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"},
	))

	InvalidVehicleCoordinatesGauge = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_invalid_vehicle_coordinates",
			Help: "Current number of GTFS-RT vehicle positions with invalid coordinates",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	StoppedOutOfBoundsVehiclesGauge = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_stopped_out_of_bounds_vehicles",
			Help: "Number of vehicles outside bounding box while stopped at a stop",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))
	TrackedVehiclesGauge = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_tracked_vehicles_count",
			Help: "Number of attributable vehicle IDs currently held in the freshness state store",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	// GtfsRtUnattributedVehicles counts RT vehicles the telemetry pass could not
	// publish a per-agency series for: those whose TripDescriptor.route_id did
	// not resolve to a server-reported agency, and those carrying no vehicle ID
	// (every per-vehicle series is keyed by vehicle_id, so an ID-less entity
	// has nowhere else to land). This is the operator's signal that their
	// static feeds are missing routes the RT feed references — typically
	// because a feed is stale or a new service was added without a matching
	// static bundle — or that the feed itself is malformed. The gauge is
	// server-scoped because we don't know the agency until attribution
	// succeeds.
	GtfsRtUnattributedVehicles = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_unattributed_vehicles_count",
			Help: "Number of GTFS-RT vehicles not attributable to exactly one agency reported by OBA",
		},
		[]string{"server_name", "server_url"},
	))

	GtfsRtUnattributedVehiclesByReason = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_unattributed_vehicles_by_reason_count",
			Help: "Number of server-mode GTFS-RT vehicles not attributable to exactly one agency reported by OBA, partitioned by bounded reason",
		},
		[]string{"server_name", "server_url", "reason"},
	))

	GtfsRtUnattributedVehicleCandidateAssociations = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_unattributed_vehicle_candidate_associations_count",
			Help: "Possible agency associations for unattributed GTFS-RT vehicles; one vehicle may contribute to multiple agency series",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))
)

var (
	GtfsStaticRefreshLastAttempt = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_refresh_last_attempt_timestamp_seconds",
		Help: "Unix timestamp of the latest complete static GTFS refresh attempt",
	}, []string{"server_name", "server_url"}))

	GtfsStaticRefreshLastAttemptSuccess = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_refresh_last_attempt_success",
		Help: "Whether the latest complete static GTFS refresh attempt succeeded",
	}, []string{"server_name", "server_url"}))

	GtfsStaticRefreshRetrying = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_refresh_retrying",
		Help: "Whether a bounded static GTFS refresh retry campaign is active",
	}, []string{"server_name", "server_url"}))

	GtfsStaticRefreshGaveUp = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_refresh_gave_up",
		Help: "Whether the current daily static GTFS refresh campaign exhausted its 12-hour retry budget",
	}, []string{"server_name", "server_url"}))

	GtfsStaticFeedFetchSuccess = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_fetch_success",
		Help: "Whether the latest fetch attempt for a configured GTFS static feed succeeded",
	}, []string{"feed_url", "server_name", "server_url"}))

	GtfsStaticFeedLastAttempt = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_last_attempt_timestamp_seconds",
		Help: "Unix timestamp of the latest fetch attempt for a configured GTFS static feed",
	}, []string{"feed_url", "server_name", "server_url"}))

	GtfsStaticFeedLastSuccessfulFetch = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_last_successful_fetch_timestamp_seconds",
		Help: "Unix timestamp of the latest successful fetch for a configured GTFS static feed",
	}, []string{"feed_url", "server_name", "server_url"}))

	GtfsStaticFeedFailureSince = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_failure_since_timestamp_seconds",
		Help: "Unix timestamp when the configured GTFS static feed's current failure streak began, or 0 when healthy",
	}, []string{"feed_url", "server_name", "server_url"}))

	GtfsStaticFeedConservativeMode = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_conservative_mode",
		Help: "Whether retries for a repeatedly failing GTFS static feed are limited to the daily recovery probe",
	}, []string{"feed_url", "server_name", "server_url"}))

	GtfsStaticFeedFailureInfo = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_static_feed_failure_info",
		Help: "Bounded classification of the latest GTFS static feed failure",
	}, []string{"feed_url", "server_name", "server_url", "stage", "reason"}))
)

// OBA REST API 2.6.0 >= Metrics
var (
	ObaMetricsLastSuccessfulFetch = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_metrics_last_successful_fetch_timestamp_seconds",
			Help: "Unix timestamp of the last successful OBA metrics response containing the configured agency",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaVehiclesLastSuccessfulFetch = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_vehicles_last_successful_fetch_timestamp_seconds",
			Help: "Unix timestamp of the last successful OBA vehicles-for-agency response",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaRealtimeRecords = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_records_count",
			Help: "Number of realtime trip/vehicle records processed during a feed update",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaRealtimeTripsMatched = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_matched_count",
			Help: "Number of matched realtime trips",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaRealtimeTripsUnmatched = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_unmatched_count",
			Help: "Number of unmatched realtime trips",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaScheduledTrips = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_scheduled_trips_count",
			Help: "Number of scheduled trips",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaStopsMatched = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_matched_count",
			Help: "Number of matched stops",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaStopsUnmatched = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_unmatched_count",
			Help: "Number of unmatched stops",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	TripMatchRatio = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_realtime_trip_match_ratio",
		Help: "Ratio of matched realtime trips to total realtime trips",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	StopMatchRatio = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_stop_match_ratio",
		Help: "Ratio of matched stops to total stops",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	ObaTimeSinceUpdate = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_time_since_last_update_seconds",
			Help: "Time since last realtime update in seconds",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	ObaUnmatchedStopInfo = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_info",
			Help: "Presence marker (always 1) for unmatched physical stop locations from static GTFS",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url", "stop_id", "stop_name", "lat", "lon"},
	))

	ObaUnmatchedStopUnresolved = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_unresolved",
			Help: "Number of stop IDs reported as unmatched by the OBA metrics API that could not be resolved against the local GTFS static bundle",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsBundleLastFetchedTimestamp = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_bundle_last_fetched_timestamp_seconds",
			Help: "Unix timestamp of when the GTFS static bundle was last downloaded for a server",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	UnmatchedStopClusterCount = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_cluster_count",
			Help: "Number of unmatched physical stop locations grouped by station and S2 spatial cluster",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url", "station_id", "cluster_id", "cluster_lat", "cluster_lon"},
	))
)

// Server-scope introspection metrics (new with the server-scope redesign).
//
// GtfsStaticStopsCount / GtfsStaticRoutesCount are emitted on every static
// refresh for every agency we have a bundle for. They let operators alert on
// sudden drops (e.g., a feed that lost all its routes overnight).
//
// GtfsStaticAgencyReportedByOBA is set on every scrape: 1 if the agency has
// a static bundle AND is reported by /api/where/metrics.json entry.AgencyIDs
// in the current scrape, 0 otherwise. The 0 case signals that an agency in
// the local static snapshot is not present in OBA's loaded static coverage.
//
// Feed-to-agency mapping metrics are emitted from static parsing, where source
// feed provenance is still available. Info series represent only proven
// relationships; failures use a bounded reason label.
var (
	GtfsStaticStopsCount = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_stops_count",
			Help: "Number of stops parsed from the agency's static GTFS bundle(s)",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsStaticRoutesCount = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_routes_count",
			Help: "Number of routes parsed from the agency's static GTFS bundle(s)",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsStaticAgencyReportedByOBA = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_agency_reported_by_oba",
			Help: "1 if the agency has a static bundle AND is reported by /api/where/metrics.json entry.AgencyIDs in the current scrape, 0 otherwise",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsStaticFeedAgencyMappingInfo = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_feed_agency_mapping_info",
			Help: "Proven relationship between a parsed GTFS static source feed and an agency",
		},
		[]string{"feed_url", "agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsStaticFeedAgencyMappingFailure = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_feed_agency_mapping_failure",
			Help: "Static source feed whose agency relationship could not be fully resolved",
		},
		[]string{"feed_url", "server_name", "server_url", "reason"},
	))
)

var (
	OutgoingLatency = tracked(promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_outgoing_request_duration_seconds",
			Help:    "Duration of outgoing HTTP requests to external APIs (in seconds)",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"url", "method", "status_code"},
	))
)
