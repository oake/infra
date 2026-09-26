// Package beacon only reads configuration links and sends one small report.
package beacon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/oake/infra/internal/api"
)

func Collect(host, platform, root string) (api.Beacon, error) {
	return collect(host, platform, func(p string) (string, error) { return filepath.EvalSymlinks(filepath.Join(root, p)) })
}

func collect(host, platform string, resolve func(string) (string, error)) (api.Beacon, error) {
	b := api.Beacon{Host: host}
	paths := []struct {
		link     string
		dest     *string
		required bool
	}{{"/run/current-system", &b.Active, true}, {"/nix/var/nix/profiles/system", &b.Profile, false}}
	if platform != "darwin" {
		paths = append(paths, struct {
			link     string
			dest     *string
			required bool
		}{"/run/booted-system", &b.Booted, false})
	}
	for _, p := range paths {
		value, e := resolve(p.link)
		if e != nil {
			if os.IsNotExist(e) && !p.required {
				continue
			}
			return b, fmt.Errorf("resolve %s: %w", p.link, e)
		}
		if !api.StorePath.MatchString(value) {
			return b, fmt.Errorf("%s resolves to invalid store path", p.link)
		}
		*p.dest = value
	}
	return b, nil
}
