package hub

import (
	"errors"
	"time"
)

func (state *State) hostAction(id, action string) error {
	h, ok := state.Hosts[id]
	if !ok {
		return errors.New("unknown host")
	}
	switch action {
	case "reboot":
		return state.QueueReboot(id, time.Now().UTC())
	case "deploy":
		if !h.Automatic || h.Platform == "darwin" || state.configurationOnMain(h) || h.Desired == "" {
			return errors.New("host is not outside main with a known deployment target")
		}
		h.DeploymentApproval = deploymentApproval(h)
		state.Hosts[id] = h
		fallthrough
	case "retry":
		for jid, j := range state.Jobs {
			if j.Kind != "reboot" && j.Host == id && j.Result != nil && (j.Result.Outcome == "failed" || j.Result.Outcome == "ambiguous") {
				j.Superseded = true
				state.Jobs[jid] = j
			}
		}
	default:
		return errors.New("unknown action")
	}
	return nil
}
