package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oake/infra/internal/api"
)

const Producer = "dix-69f91d6-schema1"

func validateSnapshot(s api.Snapshot) error {
	if s.Schema != 1 || !api.StorePath.MatchString(s.Root) {
		return errors.New("invalid snapshot schema or root")
	}
	seen := map[string]bool{}
	var total int64
	for _, p := range s.Closure {
		if !api.StorePath.MatchString(p.Path) || p.Size < 0 || seen[p.Path] || p.Size > math.MaxInt64-total {
			return errors.New("invalid closure member")
		}
		seen[p.Path] = true
		total += p.Size
	}
	if !seen[s.Root] {
		return errors.New("snapshot root missing from closure")
	}
	selected := map[string]bool{}
	for _, p := range s.Selected {
		if !seen[p] || selected[p] {
			return errors.New("invalid selected member")
		}
		selected[p] = true
	}
	return nil
}
func atomicWrite(path string, data []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".infra-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func writeSnapshot(root string, s api.Snapshot) (string, error) {
	if e := validateSnapshot(s); e != nil {
		return "", e
	}
	sort.Slice(s.Closure, func(i, j int) bool { return s.Closure[i].Path < s.Closure[j].Path })
	sort.Strings(s.Selected)
	b, e := json.Marshal(s)
	if e != nil {
		return "", e
	}
	id := api.ID(Producer, string(b))
	return id, atomicWrite(filepath.Join(root, "snapshots", id+".json"), b)
}

type PackageDiff struct {
	Explicit bool   `json:"explicit"`
	Name     string `json:"name"`
	Before   string `json:"before"`
	After    string `json:"after"`
}
type Comparison struct {
	MissingHosts []string      `json:"missing_hosts,omitempty"`
	Status       string        `json:"status"`
	Engine       string        `json:"engine"`
	Old          string        `json:"old"`
	New          string        `json:"new"`
	Packages     []PackageDiff `json:"packages"`
	SizeOld      int64         `json:"size_old"`
	SizeNew      int64         `json:"size_new"`
	Error        string        `json:"error,omitempty"`
}

func compare(ctx context.Context, s *State, root, dix string, old, next string) (Comparison, error) {
	c := Comparison{Old: old, New: next, Packages: []PackageDiff{}}
	if old == next && old != "" {
		c.Status = "unchanged"
		return c, nil
	}
	a, aok := s.Artifacts[old]
	b, bok := s.Artifacts[next]
	if !aok || !bok || a.Snapshot == "" || b.Snapshot == "" {
		c.Status = "unavailable"
		return c, nil
	}
	binary, err := exec.LookPath(dix)
	if err != nil {
		return c, fmt.Errorf("dix unavailable: %w", err)
	}
	engine, err := fingerprint(binary)
	if err != nil {
		return c, err
	}
	c.Engine = engine
	key := api.ID(a.Snapshot, b.Snapshot, engine, "package-versions-v4")
	cache := filepath.Join(root, "diffs", key+".json")
	release, err := lockDiff(ctx, cache)
	if err != nil {
		return c, err
	}
	defer release()
	if data, e := os.ReadFile(cache); e == nil {
		if e = json.Unmarshal(data, &c); e == nil {
			return c, nil
		}
	}
	oldFile := filepath.Join(root, "snapshots", a.Snapshot+".json")
	newFile := filepath.Join(root, "snapshots", b.Snapshot+".json")
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "diff-snapshots", oldFile, newFile, "--output", "json")
	var out cappedBuffer
	cmd.Stdout = &out
	var stderr cappedBuffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if e := cmd.Run(); e != nil {
		return c, fmt.Errorf("dix: %w: %s", e, stderr.String())
	}
	if !json.Valid(out.Bytes()) {
		return c, errors.New("invalid dix report")
	}
	var r struct {
		SizeOld int64 `json:"size_old"`
		SizeNew int64 `json:"size_new"`
		Diffs   []struct {
			Name      string `json:"name"`
			Selection string `json:"selection"`
			Versions  []struct {
				Kind      string `json:"kind"`
				OldAmount int    `json:"old_amount"`
				NewAmount int    `json:"new_amount"`
				Old       struct {
					Name   string `json:"name"`
					Amount int    `json:"amount"`
				} `json:"old"`
				New struct {
					Name   string `json:"name"`
					Amount int    `json:"amount"`
				} `json:"new"`
				Version struct {
					Name   string `json:"name"`
					Amount int    `json:"amount"`
				} `json:"version"`
			} `json:"versions"`
		} `json:"diffs"`
	}
	if e := json.Unmarshal(out.Bytes(), &r); e != nil {
		return c, e
	}
	var oldVersions, newVersions map[string]string
	for _, d := range r.Diffs {
		if len(d.Versions) == 0 {
			oldVersions, err = snapshotVersions(oldFile)
			if err != nil {
				return c, err
			}
			newVersions, err = snapshotVersions(newFile)
			if err != nil {
				return c, err
			}
			break
		}
	}
	c.SizeOld = r.SizeOld
	c.SizeNew = r.SizeNew
	for _, d := range r.Diffs {
		p := PackageDiff{Name: d.Name, Explicit: d.Selection == "Selected" || d.Selection == "NewlySelected" || d.Selection == "NewlyUnselected"}
		if len(d.Versions) == 0 {
			p.Before, p.After = oldVersions[d.Name], newVersions[d.Name]
		}
		for _, v := range d.Versions {
			switch v.Kind {
			case "changed":
				p.Before += versionAmount(v.Old.Name, v.Old.Amount, false) + "\n"
				p.After += versionAmount(v.New.Name, v.New.Amount, false) + "\n"
			case "added":
				p.After += versionAmount(v.Version.Name, v.Version.Amount, false) + "\n"
			case "removed":
				p.Before += versionAmount(v.Version.Name, v.Version.Amount, false) + "\n"
			case "amount_changed":
				p.Before += versionAmount(v.Version.Name, v.OldAmount, true) + "\n"
				p.After += versionAmount(v.Version.Name, v.NewAmount, true) + "\n"
			}
		}
		p.Before, p.After = strings.TrimSpace(p.Before), strings.TrimSpace(p.After)
		c.Packages = append(c.Packages, p)
	}
	c.Status = "different"
	data, e := json.Marshal(c)
	if e != nil {
		return c, e
	}
	return c, atomicWrite(cache, data)
}

func versionAmount(name string, amount int, explicit bool) string {
	if amount > 1 || explicit {
		return fmt.Sprintf("%s ×%d", name, amount)
	}
	return name
}

// Resolve unchanged versions omitted from dix's size-only diff entries.
func snapshotVersions(file string) (map[string]string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var snapshot api.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	versions := map[string]map[string]bool{}
	for _, path := range snapshot.Closure {
		_, name, ok := strings.Cut(filepath.Base(path.Path), "-")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, ".drv")
		version := "<none>"
		// Match dix's name/version split: digit-leading suffix or a git hash.
		for i := 1; i < len(name)-1; i++ {
			if name[i] != '-' {
				continue
			}
			suffix := name[i+1:]
			isHash, hasLetter := len(suffix) >= 7 && len(suffix) <= 40, false
			for _, c := range suffix {
				digit := c >= '0' && c <= '9'
				letter := c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
				isHash = isHash && (digit || letter)
				hasLetter = hasLetter || letter
			}
			if suffix[0] >= '0' && suffix[0] <= '9' || isHash && hasLetter {
				name, version = name[:i], suffix
				break
			}
		}
		if versions[name] == nil {
			versions[name] = map[string]bool{}
		}
		versions[name][version] = true
	}
	out := map[string]string{}
	for name, set := range versions {
		values := make([]string, 0, len(set))
		for version := range set {
			values = append(values, version)
		}
		sort.Strings(values)
		out[name] = strings.Join(values, "\n")
	}
	return out, nil
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<20 {
		return 0, errors.New("output too large")
	}
	return b.Buffer.Write(p)
}
