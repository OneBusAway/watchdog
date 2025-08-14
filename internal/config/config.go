package config

import (
	"sync"

	"watchdog.onebusaway.org/internal/models"
)

// Config holds all the configuration settings for our application.
type Config struct {
	Port          int
	Env           string
	FetchInterval int
	Mu            sync.RWMutex
	Servers       []models.ObaServer
}

// NewConfig creates a new instance of a Config struct.
func NewConfig(port int, env string, servers []models.ObaServer) *Config {
	return &Config{
		Port:    port,
		Env:     env,
		Servers: servers,
	}
}

// UpdateConfig safely updates the config servers.
func (cfg *Config) UpdateConfig(newServers []models.ObaServer) {
	cfg.Mu.Lock()
	defer cfg.Mu.Unlock()
	cfg.Servers = newServers
}

// GetServers safely returns a copy of the servers slice to avoid
// concurrent modification issues.
// This method should be used to access the servers from other parts of the application.
// It returns a copy of the servers slice to ensure thread safety.
func (cfg *Config) GetServers() []models.ObaServer {
	cfg.Mu.RLock()
	defer cfg.Mu.RUnlock()
	return append([]models.ObaServer(nil), cfg.Servers...)
}
