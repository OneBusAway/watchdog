package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
	RealtimeVehiclePositions = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "realtime_vehicle_positions_count_gtfs_rt",
		Help: "Number of realtime vehicle positions in the GTFS-RT feed",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	AgencyActiveVehiclesGauge = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agency_active_vehicles_count",
		Help: "Number of active vehicles reported for the agency by the OBA vehicles-for-agency API",
	}, []string{"agency_id", "agency_name", "server_name", "server_url"}))

	VehicleReportInterval = tracked(promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vehicle_position_report_interval_seconds",
		Help: "Time in seconds since each vehicle last reported a GTFS-RT position",
	}, []string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"}))

	VehicleReportCount = tracked(promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vehicle_report_total",
		Help: "Total number of GTFS-RT updates received from each vehicle",
	}, []string{"vehicle_id", "agency_id", "agency_name", "server_name", "server_url", "feed"}))

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
			Help: "Number of vehicles currently being tracked (i.e., have a last seen position and timestamp)",
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
			Help: "Number of GTFS-RT vehicles not attributable to a server-reported agency_id (unresolvable route_id, or no vehicle ID)",
		},
		[]string{"server_name", "server_url"},
	))
)

// OBA REST API 2.6.0 >= Metrics
var (
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
			Help: "Presence marker (always 1) for unmatched stops from static GTFS with their location as labels",
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
			Help: "Number of unmatched stops grouped by station and S2 spatial cluster",
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
// GtfsStaticAgencyCurrentlyLive is set on every scrape: 1 if the agency has
// a static bundle AND is reported by /api/where/metrics.json entry.AgencyIDs
// in the current scrape, 0 otherwise. The 0 case is the operator's signal
// that an agency is configured but not currently served.
//
// GtfsStaticFeedAttributionStatus flips to 0 when a configured feed's
// agency_id cannot be matched to a server-reported agency (the feed's
// agency.txt has zero rows, multiple ambiguous rows, or an agency OBA no
// longer reports). Emitted per (feed_url, agency_id) so operators can tell
// exactly which feed broke.
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

	GtfsStaticAgencyCurrentlyLive = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_agency_currently_live",
			Help: "1 if the agency has a static bundle AND is reported by /api/where/metrics.json entry.AgencyIDs in the current scrape, 0 otherwise",
		},
		[]string{"agency_id", "agency_name", "server_name", "server_url"},
	))

	GtfsStaticFeedAttributionStatus = tracked(promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_static_feed_attribution_status",
			Help: "1 if the configured static feed was unambiguously attributed to a server-reported agency, 0 otherwise",
		},
		[]string{"feed_url", "agency_id", "agency_name", "server_name", "server_url"},
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
