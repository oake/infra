// Package api contains only the machine protocol and standard-library helpers.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

var StorePath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+$`)
var Repository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var Revision = regexp.MustCompile(`^[0-9a-f]{40}$`)
var Name = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func ID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type Beacon struct {
	Host    string `json:"host"`
	Active  string `json:"active"`
	Booted  string `json:"booted,omitempty"`
	Profile string `json:"profile,omitempty"`
}
type Job struct {
	Kind       string    `json:"kind,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	Repository string    `json:"repository"`
	Revision   string    `json:"revision"`
	Node       string    `json:"node"`
	System     string    `json:"system"`
	Activation string    `json:"activation"`
	Created    time.Time `json:"created"`
	NotBefore  time.Time `json:"not_before"`
	Superseded bool      `json:"superseded"`
	Result     *Result   `json:"result,omitempty"`
}
type Result struct {
	Outcome  string    `json:"outcome"` // staged, rebooting, expired, unreachable, failed, ambiguous
	Detail   string    `json:"detail"`
	Finished time.Time `json:"finished"`
}
type Mapping struct {
	Platform   string   `json:"platform,omitempty"`
	Automatic  bool     `json:"automatic,omitempty"`
	Host       string   `json:"host"`
	System     string   `json:"system"`
	Activation string   `json:"activation"`
	Checks     []string `json:"checks,omitempty"`
}
type BuildEvent struct {
	InventoryComplete bool              `json:"inventory_complete,omitempty"`
	ObservedAt        time.Time         `json:"observed_at,omitempty"`
	Errors            map[string]string `json:"errors,omitempty"`
	Outputs           map[string]string `json:"outputs"` // evaluation: complete attribute -> output path list
	LogURL            string            `json:"log_url,omitempty"`
	ID                string            `json:"id"`
	Repository        string            `json:"repository"`
	Revision          string            `json:"revision"`
	Kind              string            `json:"kind"`   // evaluation, build, ready
	Status            string            `json:"status"` // success, failed
	Detail            string            `json:"detail,omitempty"`
	Mappings          []Mapping         `json:"mappings,omitempty"`
	Artifact          string            `json:"artifact,omitempty"`
}
type Snapshot struct {
	Schema   int            `json:"schema_version"`
	Root     string         `json:"root"`
	Closure  []SnapshotPath `json:"closure"`
	Selected []string       `json:"selected"`
}
type SnapshotPath struct {
	Path string `json:"path"`
	Size int64  `json:"nar_size"`
}
