package gtfs

import (
	"fmt"
	"log/slog"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

type agencyStaticResult struct {
	data        *models.StaticData
	routeIDs    map[string]string
	tripIDs     map[string]string
	agencyNames map[string]string
}

// buildAgencyStaticSnapshot walks the parser graph while it is still available
// and returns an owned, agency-scoped static snapshot. Trips and stop times are
// used only to select services and stops; they are not retained in the result.
func buildAgencyStaticSnapshot(server models.ObaServer, bundles []*remoteGtfs.Static, logger *slog.Logger) agencyStaticResult {
	routeIDs, tripIDs := buildAttributionMaps(server, bundles, true, logger)
	result := agencyStaticResult{
		data:        &models.StaticData{},
		routeIDs:    routeIDs,
		tripIDs:     tripIDs,
		agencyNames: make(map[string]string),
	}

	agency := configuredAgency(server, bundles)
	result.data.Agencies = []remoteGtfs.Agency{agency}
	result.agencyNames[server.AgencyID] = agency.Name

	selectedRoutes := make([]remoteGtfs.Route, 0)
	seenRoutes := make(map[string]struct{})
	selectedServices := make([]*remoteGtfs.Service, 0)
	seenServices := make(map[*remoteGtfs.Service]struct{})
	selectedStops := make([]*remoteGtfs.Stop, 0)
	seenStops := make(map[string]struct{})

	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}
		for i := range bundle.Routes {
			route := &bundle.Routes[i]
			if route.Id == "" || routeIDs[route.Id] != server.AgencyID {
				continue
			}
			if _, seen := seenRoutes[route.Id]; seen {
				continue
			}
			seenRoutes[route.Id] = struct{}{}
			selectedRoutes = append(selectedRoutes, *route)
		}
		for i := range bundle.Trips {
			trip := &bundle.Trips[i]
			if trip.ID == "" || tripIDs[trip.ID] != server.AgencyID {
				continue
			}
			if trip.Service != nil {
				if _, seen := seenServices[trip.Service]; !seen {
					seenServices[trip.Service] = struct{}{}
					selectedServices = append(selectedServices, trip.Service)
				}
			}
			for i := range trip.StopTimes {
				addStopAndParents(trip.StopTimes[i].Stop, &selectedStops, seenStops)
			}
		}
	}

	result.data.Routes = make([]remoteGtfs.Route, len(selectedRoutes))
	for i := range selectedRoutes {
		result.data.Routes[i] = cloneRoute(selectedRoutes[i])
		result.data.Routes[i].Agency = &result.data.Agencies[0]
	}
	result.data.Services = make([]remoteGtfs.Service, len(selectedServices))
	for i, service := range selectedServices {
		result.data.Services[i] = cloneService(*service)
	}
	result.data.Stops = make([]remoteGtfs.Stop, len(selectedStops))
	stopByID := make(map[string]*remoteGtfs.Stop, len(selectedStops))
	for i, source := range selectedStops {
		result.data.Stops[i] = cloneStop(*source)
		stopByID[result.data.Stops[i].Id] = &result.data.Stops[i]
	}
	for i, source := range selectedStops {
		if source.Parent != nil {
			parent := stopByID[source.Parent.Id]
			if parent != nil && parent != &result.data.Stops[i] && !stopParentCycle(parent, &result.data.Stops[i]) {
				result.data.Stops[i].Parent = parent
			}
		}
	}

	return result
}

func stopParentCycle(parent, child *remoteGtfs.Stop) bool {
	for current := parent; current != nil; current = current.Parent {
		if current == child {
			return true
		}
	}
	return false
}

func configuredAgency(server models.ObaServer, bundles []*remoteGtfs.Static) remoteGtfs.Agency {
	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}
		for _, agency := range bundle.Agencies {
			if agency.Id == server.AgencyID && agency.Id != "" {
				if agency.Name == "" {
					agency.Name = server.AgencyName
				}
				return agency
			}
		}
	}
	for _, bundle := range bundles {
		if bundle != nil && len(bundle.Agencies) == 1 && bundle.Agencies[0].Id == "" {
			agency := bundle.Agencies[0]
			agency.Id = server.AgencyID
			if agency.Name == "" {
				agency.Name = server.AgencyName
			}
			return agency
		}
	}
	return remoteGtfs.Agency{Id: server.AgencyID, Name: server.AgencyName}
}

func buildAttributionMaps(server models.ObaServer, bundles []*remoteGtfs.Static, agencyMode bool, logger *slog.Logger) (map[string]string, map[string]string) {
	routes := make(map[string]string)
	ambiguousRoutes := make(map[string]struct{})
	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}
		for i := range bundle.Routes {
			route := &bundle.Routes[i]
			if route.Id == "" {
				continue
			}
			agencyID := effectiveRouteAgency(server, bundle, route, agencyMode)
			if agencyID == "" {
				continue
			}
			addAttribution(routes, ambiguousRoutes, route.Id, agencyID, "route", server, logger)
		}
	}

	trips := make(map[string]string)
	ambiguousTrips := make(map[string]struct{})
	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}
		for i := range bundle.Trips {
			trip := &bundle.Trips[i]
			if trip.ID == "" || trip.Route == nil || trip.Route.Id == "" {
				continue
			}
			agencyID, ok := routes[trip.Route.Id]
			if !ok || agencyID == "" {
				continue
			}
			addAttribution(trips, ambiguousTrips, trip.ID, agencyID, "trip", server, logger)
		}
	}
	return routes, trips
}

func effectiveRouteAgency(server models.ObaServer, bundle *remoteGtfs.Static, route *remoteGtfs.Route, agencyMode bool) string {
	if route.Agency != nil && route.Agency.Id != "" {
		return route.Agency.Id
	}
	if agencyMode && len(bundle.Agencies) == 1 {
		return server.AgencyID
	}
	return ""
}

func addAttribution(values map[string]string, ambiguous map[string]struct{}, identifier, agencyID, kind string, server models.ObaServer, logger *slog.Logger) {
	if _, isAmbiguous := ambiguous[identifier]; isAmbiguous {
		return
	}
	if existing, exists := values[identifier]; exists {
		if existing == agencyID {
			return
		}
		delete(values, identifier)
		ambiguous[identifier] = struct{}{}
		if logger != nil {
			logger.Warn("Ambiguous GTFS attribution identifier", "kind", kind, "identifier", identifier, "existing_agency_id", existing, "duplicate_agency_id", agencyID, "server_name", server.ServerName)
		}
		report.ReportErrorWithSentryOptions(
			fmt.Errorf("ambiguous %s identifier %q maps to agencies %q and %q", kind, identifier, existing, agencyID),
			report.SentryReportOptions{
				Tags:         map[string]string{"server_name": server.ServerName, "identifier_kind": kind},
				ExtraContext: map[string]interface{}{"identifier": identifier, "existing_agency_id": existing, "duplicate_agency_id": agencyID},
				Level:        sentry.LevelWarning,
			},
		)
		return
	}
	values[identifier] = agencyID
}

func addStopAndParents(stop *remoteGtfs.Stop, selected *[]*remoteGtfs.Stop, seen map[string]struct{}) {
	visited := make(map[*remoteGtfs.Stop]struct{})
	for stop != nil {
		if _, cycle := visited[stop]; cycle {
			return
		}
		visited[stop] = struct{}{}
		if stop.Id != "" {
			if _, exists := seen[stop.Id]; !exists {
				seen[stop.Id] = struct{}{}
				*selected = append(*selected, stop)
			}
		}
		stop = stop.Parent
	}
}

func cloneRoute(route remoteGtfs.Route) remoteGtfs.Route {
	cloned := route
	if route.SortOrder != nil {
		value := *route.SortOrder
		cloned.SortOrder = &value
	}
	cloned.Agency = nil
	return cloned
}

func cloneService(service remoteGtfs.Service) remoteGtfs.Service {
	service.AddedDates = append([]time.Time(nil), service.AddedDates...)
	service.RemovedDates = append([]time.Time(nil), service.RemovedDates...)
	return service
}

func cloneStop(stop remoteGtfs.Stop) remoteGtfs.Stop {
	cloned := stop
	if stop.Latitude != nil {
		value := *stop.Latitude
		cloned.Latitude = &value
	}
	if stop.Longitude != nil {
		value := *stop.Longitude
		cloned.Longitude = &value
	}
	cloned.Parent = nil
	return cloned
}
