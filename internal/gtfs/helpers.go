package gtfs

import (
	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// cloneStaticData makes a self-contained copy of the reduced static fields.
// The parser graph uses pointers from routes to agencies and stops to parent
// stations; copying only the slices would keep pointers into that graph alive.
func cloneStaticData(source *models.StaticData) *models.StaticData {
	result := &models.StaticData{}
	if source == nil {
		return result
	}

	result.Agencies = append([]remoteGtfs.Agency(nil), source.Agencies...)
	agenciesByID := make(map[string]*remoteGtfs.Agency, len(result.Agencies))
	for i := range result.Agencies {
		if _, exists := agenciesByID[result.Agencies[i].Id]; !exists {
			agenciesByID[result.Agencies[i].Id] = &result.Agencies[i]
		}
	}

	result.Stops = make([]remoteGtfs.Stop, len(source.Stops))
	stopsByID := make(map[string]*remoteGtfs.Stop, len(result.Stops))
	for i := range source.Stops {
		result.Stops[i] = cloneStop(source.Stops[i])
		if _, exists := stopsByID[result.Stops[i].Id]; !exists {
			stopsByID[result.Stops[i].Id] = &result.Stops[i]
		}
	}
	for i := range source.Stops {
		if source.Stops[i].Parent == nil {
			continue
		}
		parent := stopsByID[source.Stops[i].Parent.Id]
		child := &result.Stops[i]
		if parent != nil && parent != child && !stopParentCycle(parent, child) {
			child.Parent = parent
		}
	}

	result.Routes = make([]remoteGtfs.Route, len(source.Routes))
	for i := range source.Routes {
		result.Routes[i] = cloneRoute(source.Routes[i])
		if source.Routes[i].Agency != nil {
			result.Routes[i].Agency = agenciesByID[source.Routes[i].Agency.Id]
		}
	}
	result.Services = make([]remoteGtfs.Service, len(source.Services))
	for i := range source.Services {
		result.Services[i] = cloneService(source.Services[i])
	}
	return result
}

func cloneCandidateMap(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func cloneNameMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
