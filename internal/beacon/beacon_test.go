package beacon

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformPaths(t *testing.T) {
	p := "/nix/store/" + strings.Repeat("a", 32) + "-system"
	for _, platform := range []string{"linux", "darwin"} {
		visited := map[string]bool{}
		b, e := collect("owner/repo/host", platform, func(link string) (string, error) {
			visited[link] = true
			if link == "/nix/var/nix/profiles/system" {
				return "", os.ErrNotExist
			}
			return p, nil
		})
		if e != nil {
			t.Fatal(e)
		}
		if b.Active != p || b.Profile != "" {
			t.Fatal("unexpected paths")
		}
		if platform == "darwin" && (b.Booted != "" || visited["/run/booted-system"]) {
			t.Fatal("Darwin booted path invented")
		}
		if platform == "linux" && b.Booted != p {
			t.Fatal("booted path missing")
		}
	}
}
func TestMissingActiveDoesNotSendEmptyObservation(t *testing.T) {
	if _, e := collect("owner/repo/host", "linux", func(string) (string, error) { return "", os.ErrNotExist }); e == nil {
		t.Fatal("active configuration is required")
	}
}
