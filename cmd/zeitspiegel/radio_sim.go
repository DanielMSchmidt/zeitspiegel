package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/fleet"
)

// simRadio is a virtual radio backed by a shared directory. It exists so the
// election can be exercised end to end against real binaries on a laptop —
// three actual processes, real HTTP between them, only the radio faked
// (ST-7..ST-12).
//
// It is faithful in the ways that decide the design:
//   - a unit that is beaconing cannot scan, so a primary is blind to a second
//     primary on the same SSID;
//   - two units can beacon at once, so a split brain is reproducible rather
//     than impossible;
//   - a beacon that stops being refreshed goes stale, so SIGKILLing a process
//     takes its network off the air exactly as pulling a plug would.
//
// Enabled only by the --net-sim flag, which is a development and test tool.
type simRadio struct {
	dir     string
	id      string
	baseURL string

	mu     sync.Mutex
	mode   string // "", "ap", "sta"
	stopAP context.CancelFunc
}

const (
	// simBeaconEvery is how often a hosting unit refreshes its beacon file.
	simBeaconEvery = 100 * time.Millisecond
	// simBeaconStale is how long a beacon survives without a refresh. A
	// killed process stops refreshing, and its network disappears — which is
	// what makes SIGKILL in the E2E lane behave like pulling a plug.
	simBeaconStale = time.Second
)

func newSimRadio(dir, id, baseURL string) (*simRadio, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("net-sim dir: %w", err)
	}
	return &simRadio{dir: dir, id: id, baseURL: baseURL}, nil
}

func (r *simRadio) beaconPath() string { return filepath.Join(r.dir, r.id+".ap") }

// liveAP returns the base URL of another unit currently beaconing, if any.
func (r *simRadio) liveAP() (string, bool) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ap") || e.Name() == r.id+".ap" {
			continue
		}
		path := filepath.Join(r.dir, e.Name())
		info, err := os.Stat(path)
		if err != nil || time.Since(info.ModTime()) > simBeaconStale {
			continue // gone off the air
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if url := strings.TrimSpace(string(body)); url != "" {
			return url, true
		}
	}
	return "", false
}

func (r *simRadio) Scan(context.Context) ([]string, error) {
	r.mu.Lock()
	mode := r.mode
	r.mu.Unlock()
	if mode == "ap" {
		return nil, fleet.ErrScanUnavailable
	}
	if _, ok := r.liveAP(); ok {
		return []string{simSSID}, nil
	}
	return nil, nil
}

func (r *simRadio) ActivateAP(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == "ap" {
		return nil
	}
	if err := r.writeBeacon(); err != nil {
		return err
	}
	// Keep refreshing so the beacon stays live only while this process does.
	beaconCtx, cancel := context.WithCancel(context.Background())
	r.stopAP = cancel
	go func() {
		t := time.NewTicker(simBeaconEvery)
		defer t.Stop()
		for {
			select {
			case <-beaconCtx.Done():
				return
			case <-t.C:
				r.mu.Lock()
				if r.mode == "ap" {
					_ = r.writeBeacon()
				}
				r.mu.Unlock()
			}
		}
	}()
	r.mode = "ap"
	return nil
}

// writeBeacon must be called with the lock held.
func (r *simRadio) writeBeacon() error {
	if err := os.WriteFile(r.beaconPath(), []byte(r.baseURL), 0o644); err != nil {
		return fmt.Errorf("beacon: %w", err)
	}
	return nil
}

func (r *simRadio) ActivateSTA(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopBeaconLocked()
	r.mode = "sta"
	return nil
}

func (r *simRadio) Down(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopBeaconLocked()
	r.mode = ""
	return nil
}

func (r *simRadio) stopBeaconLocked() {
	if r.stopAP != nil {
		r.stopAP()
		r.stopAP = nil
	}
	_ = os.Remove(r.beaconPath())
}

func (r *simRadio) Associated(context.Context) (bool, error) {
	r.mu.Lock()
	mode := r.mode
	r.mu.Unlock()
	if mode != "sta" {
		return false, nil
	}
	_, ok := r.liveAP()
	return ok, nil
}

func (r *simRadio) Gateway(context.Context) (string, error) {
	if url, ok := r.liveAP(); ok {
		return url, nil
	}
	return "", errors.New("no gateway: nothing is hosting the network")
}
