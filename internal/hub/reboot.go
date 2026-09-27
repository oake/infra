package hub

import (
	"crypto/rand"
	"errors"
	"time"

	"github.com/oake/infra/internal/api"
)

// Each explicit reboot request has one deadline, retained across retries.
func (s *State) QueueReboot(id string, now time.Time) error {
	s.Reconcile(now)
	h, ok := s.Hosts[id]
	if !ok || h.Removed || h.Platform == "darwin" {
		return errors.New("host cannot be rebooted")
	}
	if h.Status == "Reboot queued" {
		return nil
	}
	if h.Status != "Reboot to apply" {
		return errors.New("host has no configuration awaiting reboot")
	}
	jid := api.ID(id, "reboot", rand.Text())
	s.Jobs[jid] = api.Job{ID: jid, Kind: "reboot", Host: id, Repository: h.Repository, Revision: h.Revision, Node: h.Node, Created: now, NotBefore: now, Expires: now.Add(5 * time.Minute)}
	return nil
}

func (s *State) reconcileReboots(h Host) bool {
	queued := false
	for id, j := range s.Jobs {
		if j.Kind != "reboot" || j.Host != h.ID || j.Superseded {
			continue
		}
		observed := j.Result != nil && (j.Result.Outcome == "rebooting" || j.Result.Outcome == "ambiguous") && h.LastSeen.After(j.Result.Finished)
		if h.Removed || observed || (j.Result != nil && j.Result.Outcome == "expired") {
			j.Superseded = true
			s.Jobs[id] = j
			continue
		}
		// Acknowledged or uncertain commands await a beacon; never send them twice.
		queued = true
	}
	return queued
}
