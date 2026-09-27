package beacon

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/oake/infra/internal/api"
)

// Report records only successful delivery. A failed send leaves the previous
// receipt untouched, so the next check retries the latest observed paths.
func Report(state string, observation api.Beacon, send func() error) error {
	b, err := os.ReadFile(state)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var previous api.Beacon
	if err == nil && json.Unmarshal(b, &previous) == nil && previous == observation {
		return nil
	}
	if err := send(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return err
	}
	b, err = json.Marshal(observation)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(state), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), state)
}
