package app

import (
	"sync"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// KnownServerSet is Watchdog's memory of the configurations it has seen. It
// holds two of them, because the two questions a refresh asks need different
// spans of history:
//
//   - keys: the identity (ServerKey) of every entry in the most recently seen
//     configuration, replaced wholesale on each Diff. That is what makes
//     "which entries are genuinely new?" answerable.
//   - everConfiguredURLs: every oba_base_url ever configured, accumulated and
//     never forgotten. That is what makes "which series must not be on
//     /metrics?" answerable on *every* refresh rather than only on the one
//     that happened to see a store key disappear — see DepartedURLs.
//
// It is written from the --config-url refresh goroutine while the collection
// goroutine reads the stores, so it is mutex-guarded like the other shared
// stores.
type KnownServerSet struct {
	mu                 sync.RWMutex
	keys               map[string]bool
	everConfiguredURLs map[string]bool
}

// NewKnownServerSet creates a set seeded with the given servers. Seeding it
// with the boot-time configuration is what keeps the first refresh after
// start-up from re-downloading every bundle the process just fetched, and it
// is what lets the first refresh retire a server that was configured at boot
// and dropped before Watchdog ever recorded a config of its own.
func NewKnownServerSet(servers []models.ObaServer) *KnownServerSet {
	set := &KnownServerSet{
		keys:               make(map[string]bool, len(servers)),
		everConfiguredURLs: make(map[string]bool, len(servers)),
	}
	set.Diff(servers)
	return set
}

// Diff returns the servers that were not in the previously
// recorded set, in configuration order, and replaces the recorded set with the
// identities given here. An entry repeated within one config is reported at
// most once.
//
// It also folds this configuration's base URLs into the accumulating
// ever-configured memory, so recording a configuration by either entry point
// leaves the set complete.
func (s *KnownServerSet) Diff(servers []models.ObaServer) []models.ObaServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rememberURLs(servers)

	current := make(map[string]bool, len(servers))
	var added []models.ObaServer
	for _, server := range servers {
		key := server.ServerKey()
		if current[key] {
			continue
		}
		current[key] = true
		if !s.keys[key] {
			added = append(added, server)
		}
	}
	s.keys = current
	return added
}

// DepartedURLs records the base URLs of the given configuration and returns
// every base URL this set has ever recorded that the given configuration does
// not contain.
//
// Answering from an accumulating memory rather than from "which store keys
// went away this time" is what makes series retirement self-healing. A
// collection tick snapshots the server list at its top, so a config refresh
// can land mid-tick and the in-flight tick can write a departed server's
// gauges again after they were retired. When that write is a failed ping it
// touches no store at all, so a store-driven prune never notices and the
// series stays on /metrics frozen at 0 forever. Reporting the departure on
// every refresh instead means the resurrected series is retired by the next
// one, roughly a minute later, whatever resurrected it.
//
// Memory bound: one map entry per distinct sanitized oba_base_url ever
// configured in this process's lifetime. Entries are never dropped — dropping
// one is exactly what re-opens the race — so the set grows only when an
// operator configures a base URL that has never been configured before, and
// is bounded in practice by the size of the deployment's server roster.
func (s *KnownServerSet) DepartedURLs(servers []models.ObaServer) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rememberURLs(servers)

	configured := make(map[string]bool, len(servers))
	for _, server := range servers {
		configured[utils.SanitizeServerURL(server.ObaBaseURL)] = true
	}

	var departed []string
	for serverURL := range s.everConfiguredURLs {
		if !configured[serverURL] {
			departed = append(departed, serverURL)
		}
	}
	return departed
}

// rememberURLs adds the sanitized base URL of each server to the accumulating
// memory. The caller must hold s.mu. A blank URL is skipped: oba_base_url is
// required, so a blank one identifies no server, and remembering it would
// report a phantom departure on every later refresh whose only effect is to
// scan every metric vector for series whose server_url is literally empty.
func (s *KnownServerSet) rememberURLs(servers []models.ObaServer) {
	for _, server := range servers {
		if serverURL := utils.SanitizeServerURL(server.ObaBaseURL); serverURL != "" {
			s.everConfiguredURLs[serverURL] = true
		}
	}
}

// NewlyAddedServers returns the entries of a freshly loaded configuration that
// were not in the configuration seen before it, and records the new set.
//
// Newcomer-ness is a property of the configuration, not of the stores: the
// config refresh fires its callback on every successful load, not only when
// the config changed, so asking "does this server have a bundle yet?" makes a
// server whose download keeps failing — or a server-scoped entry whose feeds
// declare no agency, which stores nothing at all — a newcomer forever, and
// spawns a duplicate download goroutine every minute. Diffing the config
// reports each addition exactly once.
//
// Identity is models.ObaServer.ServerKey(), matching the ownership rules
// PruneStaleServers follows: an agency-scoped entry is one key, so a second
// agency added on an already-configured base URL is correctly seen as new,
// while a server-scoped entry is identified by its base URL alone.
func (app *Application) NewlyAddedServers(updated []models.ObaServer) []models.ObaServer {
	return app.KnownServers.Diff(updated)
}
