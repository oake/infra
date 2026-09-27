package hub

import (
	"context"
	"strings"
)

func (s *Store) PackageBlocks(ctx context.Context) (map[string]PackageBlock, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return readUIRows[PackageBlock](ctx, tx, "SELECT id,body FROM package_blocks")
}

func filterPackages(c Comparison, rules map[string]PackageBlock) Comparison {
	packages := make([]PackageDiff, 0, len(c.Packages))
	for _, p := range c.Packages {
		blocked := false
		for _, r := range rules {
			if (r.Match == "exact" && p.Name == r.Text) || (r.Match == "starts" && strings.HasPrefix(p.Name, r.Text)) || (r.Match == "contains" && strings.Contains(p.Name, r.Text)) {
				blocked = true
				break
			}
		}
		if !blocked {
			packages = append(packages, p)
		}
	}
	c.Packages = packages
	return c
}
