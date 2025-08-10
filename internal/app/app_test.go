package app

import (
	"testing"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/models"
)

func TestUpdateConfig(t *testing.T) {
	app := &Application{
		Config: config.NewConfig(
			4000,
			"testing",
			[]models.ObaServer{},
		),
	}

	initialServers := []models.ObaServer{
		{ID: 1, Name: "Server 1"},
	}

	newServers := []models.ObaServer{
		{ID: 1, Name: "Server 1 Updated"},
		{ID: 2, Name: "Server 2"},
	}

	app.Config.UpdateConfig(initialServers)
	if len(app.Config.GetServers()) != 1 {
		t.Errorf("Expected 1 server, got %d", len(app.Config.Servers))
	}

	app.Config.UpdateConfig(newServers)
	if len(app.Config.GetServers()) != 2 {
		t.Errorf("Expected 2 servers, got %d", len(app.Config.Servers))
	}

	if app.Config.Servers[0].Name != "Server 1 Updated" {
		t.Errorf("Expected server name to be updated to 'Server 1 Updated', got %s", app.Config.Servers[0].Name)
	}
}
