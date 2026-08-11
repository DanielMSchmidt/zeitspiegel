package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/fleet"
)

// nmcliRadio drives the real radio through NetworkManager's CLI.
//
// Subprocess rather than a library binding, for the same reasons ffmpeg is
// (hard rule 7): no cgo, no binding to maintain, and a hung child cannot take
// the mirror down with it. Both connection profiles are baked into the image
// with autoconnect=false, so this binary — not NetworkManager — decides which
// one is up, and nothing has to be written to the read-only root at runtime.
type nmcliRadio struct {
	iface   string
	apConn  string
	staConn string

	mu   sync.Mutex
	mode string // "", "ap", "sta"
}

// nmcliTimeout bounds every child. Bringing an AP up is the slow one — the
// radio has to stop, reconfigure and start beaconing.
const nmcliTimeout = 30 * time.Second

func newNMCLIRadio() *nmcliRadio {
	return &nmcliRadio{iface: "wlan0", apConn: "zeitspiegel-ap", staConn: "zeitspiegel-sta"}
}

func (r *nmcliRadio) currentMode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

func (r *nmcliRadio) setMode(m string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = m
}

// Scan lists the SSIDs on the air. A radio that is beaconing cannot scan —
// that is a property of the hardware, not a transient failure, so it gets its
// own error rather than being retried away (ARCHITECTURE D8).
func (r *nmcliRadio) Scan(ctx context.Context) ([]string, error) {
	if r.currentMode() == "ap" {
		return nil, fleet.ErrScanUnavailable
	}
	out, err := r.run(ctx, "-t", "-f", "SSID", "device", "wifi", "list", "ifname", r.iface, "--rescan", "yes")
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		// -t escapes colons inside values as \: — unescape before use.
		if s := strings.TrimSpace(strings.ReplaceAll(line, `\:`, ":")); s != "" {
			ssids = append(ssids, s)
		}
	}
	return ssids, nil
}

func (r *nmcliRadio) ActivateAP(ctx context.Context) error {
	if _, err := r.run(ctx, "connection", "up", r.apConn); err != nil {
		return fmt.Errorf("bring up %s: %w", r.apConn, err)
	}
	r.setMode("ap")
	return nil
}

func (r *nmcliRadio) ActivateSTA(ctx context.Context) error {
	// Drop the AP first: one radio cannot do both, and leaving the profile
	// active makes the station activation fail in a confusing way.
	if r.currentMode() == "ap" {
		_, _ = r.run(ctx, "connection", "down", r.apConn)
	}
	if _, err := r.run(ctx, "connection", "up", r.staConn); err != nil {
		r.setMode("")
		return fmt.Errorf("bring up %s: %w", r.staConn, err)
	}
	r.setMode("sta")
	return nil
}

func (r *nmcliRadio) Down(ctx context.Context) error {
	var errs []error
	for _, conn := range []string{r.apConn, r.staConn} {
		if _, err := r.run(ctx, "connection", "down", conn); err != nil {
			// "not an active connection" is the normal case for whichever
			// profile was not up; it is not worth surfacing.
			errs = append(errs, err)
		}
	}
	r.setMode("")
	if len(errs) == 2 {
		return fmt.Errorf("bring the radio down: %w", errors.Join(errs...))
	}
	return nil
}

func (r *nmcliRadio) Associated(ctx context.Context) (bool, error) {
	if r.currentMode() != "sta" {
		return false, nil
	}
	out, err := r.run(ctx, "-t", "-f", "GENERAL.STATE", "device", "show", r.iface)
	if err != nil {
		return false, fmt.Errorf("device state: %w", err)
	}
	// GENERAL.STATE:100 (connected)
	return strings.Contains(out, "100 ("), nil
}

// Stations counts the clients associated to this unit's AP — member units
// and guests' phones alike. `iw` speaks nl80211 directly and, unlike a scan,
// this works IN AP mode, which is exactly what lets a host know whether
// anyone is using its network without ever taking it down (D8). iw is baked
// into the image. The supervisor throttles calls: frequent station dumps
// have been reported to cause momentary drops on brcmfmac in noisy RF.
func (r *nmcliRadio) Stations(ctx context.Context) (int, error) {
	if r.currentMode() != "ap" {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, nmcliTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "iw", "dev", r.iface, "station", "dump").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("iw station dump: %w: %s", err, strings.TrimSpace(string(out)))
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Station ") {
			n++
		}
	}
	return n, nil
}

// Gateway is the default route on the wireless interface, which on this
// network is the unit hosting the AP — NetworkManager's shared mode always
// takes 10.42.0.1, so a member needs no configuration to find its primary.
func (r *nmcliRadio) Gateway(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "-t", "-f", "IP4.GATEWAY", "device", "show", r.iface)
	if err != nil {
		return "", fmt.Errorf("gateway: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		_, value, ok := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		if ok && value != "" && value != "--" {
			return "http://" + value, nil
		}
	}
	return "", errors.New("gateway: no IPv4 default route yet")
}

func (r *nmcliRadio) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, nmcliTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nmcli", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nmcli %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
