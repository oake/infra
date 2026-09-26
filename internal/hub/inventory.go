package hub

import (
	"fmt"
	"strings"

	"github.com/oake/infra/internal/api"
)

// The stored inventory is derived state, never a separately maintained host list.
// Validate the full result before changing anything. Missing hosts are retired;
// their observations and deployment history remain available.
func (s *State) applyInventory(repository string, hosts []Host, complete bool) error {
	if _, ok := s.Repositories[repository]; !ok {
		return fmt.Errorf("unknown repository")
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if !api.Name.MatchString(h.Name) || seen[h.Name] || (h.Platform != "nixos" && h.Platform != "darwin") {
			return fmt.Errorf("invalid flake host %q", h.Name)
		}
		seen[h.Name] = true
	}
	for id, h := range s.Hosts {
		if complete && h.Repository == repository {
			h.Removed = !seen[h.Name]
			s.Hosts[id] = h
		}
	}
	for _, derived := range hosts {
		id := repository + "/" + derived.Name
		h := s.Hosts[id]
		h.ID, h.Name, h.Node, h.Repository = id, derived.Name, derived.Name, repository
		h.Platform, h.Automatic, h.Removed = derived.Platform, derived.Automatic, false
		s.Hosts[id] = h
	}
	return nil
}

// Only current main changes inventory. Valid partial mappings update known facts;
// only a complete inventory can retire absent hosts.
func (s *State) inventoryFromCommit(c Commit) error {
	if c.Revision != s.Repositories[c.Repository].Main {
		return nil
	}
	hosts := []Host{}
	for _, m := range c.Mappings {
		// Imported historical mappings contain paths without inventory metadata.
		if m.Platform == "" && !c.InventoryComplete {
			continue
		}
		hosts = append(hosts, Host{Name: strings.TrimPrefix(m.Host, c.Repository+"/"), Platform: m.Platform, Automatic: m.Automatic})
	}
	return s.applyInventory(c.Repository, hosts, c.InventoryComplete)
}
