package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oake/infra/internal/api"
)

// Most beacons only renew last_seen. Do not load commit/artifact/job history for
// an unchanged observation. Changed paths use the normal state transition.
func (s *Store) Observe(ctx context.Context, b api.Beacon, now time.Time) error {
	unchanged, err := s.renewObservation(ctx, b, now)
	if err != nil || unchanged {
		return err
	}
	return s.Update(ctx, func(st *State) error {
		if err := st.Observe(b, now); err != nil {
			return err
		}
		st.Reconcile(now)
		return nil
	})
}

func (s *Store) renewObservation(ctx context.Context, b api.Beacon, now time.Time) (bool, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM hosts WHERE id=$1", b.Host).Scan(&raw); err != nil {
		return false, err
	}
	var h Host
	if err = json.Unmarshal(raw, &h); err != nil {
		return false, err
	}
	probe := NewState()
	probe.Hosts[b.Host] = h
	if err = probe.Observe(b, now); err != nil {
		return false, err
	}
	if h.Observation == b && !h.LastSeen.Before(h.StagedAt) {
		_, err = tx.ExecContext(ctx, `UPDATE hosts SET body=json_set(body,'$.last_seen',$2) WHERE id=$1`, b.Host, now.Format(time.RFC3339Nano))
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	return false, nil
}

func (s *Store) Outstanding(ctx context.Context, repo string, now time.Time) ([]api.Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT body FROM jobs WHERE body->>'repository'=$1
 AND json_extract(body,'$.result') IS NULL
 AND COALESCE(body->>'superseded',0)=0
 AND julianday(body->>'not_before') <= julianday($2) ORDER BY julianday(body->>'created'),id`, repo, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []api.Job{}
	for rows.Next() {
		var raw []byte
		var job api.Job
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
