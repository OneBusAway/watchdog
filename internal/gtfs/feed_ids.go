package gtfs

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfscsv "github.com/OneBusAway/go-gtfs/csv"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
)

type duplicateIDError struct {
	kind string
	id   string
}

func (e *duplicateIDError) Error() string {
	return fmt.Sprintf("merged feed contains duplicate %s %q", e.kind, e.id)
}

// validateStaticIDs reads primary keys before go-gtfs can collapse them into
// its lookup maps. References may repeat: for example a service can have many
// calendar_dates rows and a trip can have many stop_times rows.
func validateStaticIDs(data []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	keys := map[string]string{
		"agency.txt": "agency_id", "stops.txt": "stop_id",
		"routes.txt": "route_id", "trips.txt": "trip_id", "calendar.txt": "service_id",
	}
	files := make(map[string]bool)
	for _, file := range reader.File {
		name := file.Name
		if _, needed := scheduleTables[name]; !needed {
			continue
		}
		if files[name] {
			return &duplicateIDError{kind: "table", id: name}
		}
		files[name] = true
		if key, hasPrimaryID := keys[name]; hasPrimaryID {
			if err := validateStaticTableIDs(file, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStaticTableIDs(file *zip.File, key string) error {
	body, err := file.Open()
	if err != nil {
		return err
	}
	defer body.Close()
	reader := gtfscsv.BOMAwareCSVReader(body)
	header, err := reader.Read()
	if err == io.EOF {
		return nil // Required tables and columns are checked by the parsers.
	}
	if err != nil {
		return err
	}
	column := -1
	for i, name := range header {
		if strings.TrimPrefix(strings.TrimSpace(name), "\ufeff") == key {
			column = i
			break
		}
	}
	if column < 0 {
		return nil // A sole agency may omit agency_id.
	}
	reader.ReuseRecord = true
	seen := make(map[string]struct{})
	for {
		row, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if column >= len(row) {
			return fmt.Errorf("%s row has no %s column", file.Name, key)
		}
		id := row[column]
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			return &duplicateIDError{kind: key, id: id}
		}
		seen[strings.Clone(id)] = struct{}{}
	}
}

// excludeDuplicateRealtimeIDs drops every conflicting entity rather than
// choosing a winner. Trip-update references do not make a vehicle ID duplicate,
// but references to a duplicated position ID must also be excluded.
func excludeDuplicateRealtimeIDs(feed *gtfsrt.FeedMessage) ([]*gtfsrt.FeedEntity, error) {
	entities := make(map[string]int)
	vehicles := make(map[string]int)
	var problem error
	for _, entity := range feed.Entity {
		if id := entity.GetId(); id != "" {
			entities[id]++
			if entities[id] == 2 && problem == nil {
				problem = &duplicateIDError{kind: "entity_id", id: id}
			}
		}
		if id := entity.GetVehicle().GetVehicle().GetId(); id != "" {
			vehicles[id]++
			if vehicles[id] == 2 && problem == nil {
				problem = &duplicateIDError{kind: "vehicle_id", id: id}
			}
		}
	}
	if problem == nil {
		return feed.Entity, nil
	}
	kept := make([]*gtfsrt.FeedEntity, 0, len(feed.Entity))
	for _, entity := range feed.Entity {
		if entities[entity.GetId()] > 1 || vehicles[entity.GetVehicle().GetVehicle().GetId()] > 1 || vehicles[entity.GetTripUpdate().GetVehicle().GetId()] > 1 {
			continue
		}
		kept = append(kept, entity)
	}
	return kept, problem
}

// validateParsedStaticIDs also protects callers publishing an already parsed
// bundle. Raw validation remains necessary because parser maps can hide rows.
func validateParsedStaticIDs(bundle *remoteGtfs.Static) error {
	seen := make(map[string]map[string]bool)
	check := func(kind, id string) error {
		if id == "" {
			return nil
		}
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		if seen[kind][id] {
			return &duplicateIDError{kind: kind, id: id}
		}
		seen[kind][id] = true
		return nil
	}
	for _, agency := range bundle.Agencies {
		if err := check("agency_id", agency.Id); err != nil {
			return err
		}
	}
	for _, stop := range bundle.Stops {
		if err := check("stop_id", stop.Id); err != nil {
			return err
		}
	}
	for _, route := range bundle.Routes {
		if err := check("route_id", route.Id); err != nil {
			return err
		}
	}
	for _, trip := range bundle.Trips {
		if err := check("trip_id", trip.ID); err != nil {
			return err
		}
	}
	return nil
}
