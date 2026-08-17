package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ObaApiStatus API Status (up/down)
	ObaApiStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_api_status",
			Help: "Status of the OneBusAway API Server (0 = not working, 1 = working)",
		},
		[]string{"agency_id", "agency_name", "server_url"},
	)
)

var (
	BundleEarliestExpirationGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_earliest_expiration",
		Help: "Number of days until the earliest GTFS bundle expiration",
	}, []string{"agency_id", "agency_name"})

	BundleLatestExpirationGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_latest_expiration",
		Help: "Number of days until the latest GTFS bundle expiration",
	}, []string{"agency_id", "agency_name"})
)

var (
	AgenciesTrackedCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "oba_tracked_agencies_count",
		Help: "Number of agencies currently tracked by Watchdog (validated config entries)",
	})

	AgenciesTrackedInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_tracked_agencies_info",
		Help: "One series per agency currently tracked by Watchdog (always 1)",
	}, []string{"agency_id", "agency_name", "server_url"})
)

var (
	RealtimeVehiclePositions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "realtime_vehicle_positions_count_gtfs_rt",
		Help: "Number of realtime vehicle positions in the GTFS-RT feed",
	}, []string{"agency_id", "agency_name"})

	AgencyActiveVehiclesGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agency_active_vehicles_count",
		Help: "Number of active vehicles reported for the agency by the OBA vehicles-for-agency API",
	}, []string{"agency_id", "agency_name"})

	VehicleReportInterval = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vehicle_position_report_interval_seconds",
		Help: "Time in seconds since each vehicle last reported a GTFS-RT position",
	}, []string{"vehicle_id", "agency_id", "agency_name"})

	VehicleReportCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vehicle_report_total",
		Help: "Total number of GTFS-RT updates received from each vehicle",
	}, []string{"vehicle_id", "agency_id", "agency_name"})

	VehicleSpeedGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_vehicle_computed_speed",
			Help: "Computed speed of the vehicle in m/s based on GTFS-RT positions and timestamps",
		},
		[]string{"vehicle_id", "agency_id", "agency_name"},
	)

	VehicleSpeedDiscrepancyRatioGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_vehicle_speed_discrepancy_ratio",
			Help: "Ratio between computed and reported speed (|computed - reported| / reported)",
		},
		[]string{"vehicle_id", "agency_id", "agency_name"},
	)

	InvalidVehicleCoordinatesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_invalid_vehicle_coordinates",
			Help: "Current number of GTFS-RT vehicle positions with invalid coordinates",
		},
		[]string{"agency_id", "agency_name"},
	)

	StoppedOutOfBoundsVehiclesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_stopped_out_of_bounds_vehicles",
			Help: "Number of vehicles outside bounding box while stopped at a stop",
		},
		[]string{"agency_id", "agency_name"},
	)
	TrackedVehiclesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_tracked_vehicles_count",
			Help: "Number of vehicles currently being tracked (i.e., have a last seen position and timestamp)",
		},
		[]string{"agency_id", "agency_name"},
	)
)

// OBA REST API 2.6.0 >= Metrics
var (
	ObaRealtimeRecords = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_records_count",
			Help: "Total number of realtime records",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaRealtimeTripsMatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_matched_count",
			Help: "Number of matched realtime trips",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaRealtimeTripsUnmatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_unmatched_count",
			Help: "Number of unmatched realtime trips",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaScheduledTrips = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_scheduled_trips_count",
			Help: "Number of scheduled trips",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaStopsMatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_matched_count",
			Help: "Number of matched stops",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaStopsUnmatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_unmatched_count",
			Help: "Number of unmatched stops",
		},
		[]string{"agency_id", "agency_name"},
	)

	TripMatchRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_realtime_trip_match_ratio",
		Help: "Ratio of matched realtime trips to total realtime trips",
	}, []string{"agency_id", "agency_name"})

	StopMatchRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_stop_match_ratio",
		Help: "Ratio of matched stops to total stops",
	}, []string{"agency_id", "agency_name"})

	ObaTimeSinceUpdate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_time_since_last_update_seconds",
			Help: "Time since last realtime update in seconds",
		},
		[]string{"agency_id", "agency_name"},
	)

	ObaUnmatchedStopInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_info",
			Help: "Presence marker (always 1) for unmatched stops from static GTFS with their location as labels",
		},
		[]string{"agency_id", "agency_name", "stop_id", "stop_name", "lat", "lon"},
	)

	ObaUnmatchedStopUnresolved = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_unresolved",
			Help: "Number of stop IDs reported as unmatched by the OBA metrics API that could not be resolved against the local GTFS static bundle",
		},
		[]string{"agency_id", "agency_name"},
	)

	GtfsBundleLastFetchedTimestamp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_bundle_last_fetched_timestamp_seconds",
			Help: "Unix timestamp of when the GTFS static bundle was last downloaded for a server",
		},
		[]string{"agency_id", "agency_name"},
	)

	UnmatchedStopClusterCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_cluster_count",
			Help: "Number of unmatched stops grouped by station and S2 spatial cluster",
		},
		[]string{"agency_id", "agency_name", "station_id", "cluster_id", "cluster_lat", "cluster_lon"},
	)
)

var (
	OutgoingLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_outgoing_request_duration_seconds",
			Help:    "Duration of outgoing HTTP requests to external APIs (in seconds)",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"url", "method", "status_code"},
	)
)
