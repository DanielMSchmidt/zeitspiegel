package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

func writeFleet(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// UT-20: the network keys parse; there is deliberately no fleet size — how
// many units share the network is discovered at runtime, not configured, so
// one image serves an installation that grows over time (E-8).
func TestNetworkKeysLoad(t *testing.T) {
	c, err := config.Load(writeFleet(t, `
network_manage = true
name_file = "/boot/firmware/zeitspiegel-name.txt"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.NetworkManage {
		t.Error("NetworkManage = false, want true")
	}
	if c.NameFile != "/boot/firmware/zeitspiegel-name.txt" {
		t.Errorf("NameFile = %q", c.NameFile)
	}
}

// UT-20: defaults — a dev build must never touch the radio.
func TestNetworkDefaults(t *testing.T) {
	c := config.Default()
	if c.NetworkManage {
		t.Error("NetworkManage = true, want false")
	}
	if c.NameFile != "" {
		t.Errorf("NameFile = %q, want empty (identity picks the default path)", c.NameFile)
	}
}

// UT-20: cards flashed with the previous release carry `fleet_size` in their
// config. The key is gone, and an already-flashed appliance must keep booting
// — unknown keys are ignored, never a startup error.
func TestLegacyFleetSizeKeyIsIgnored(t *testing.T) {
	c, err := config.Load(writeFleet(t, `
fleet_size = 3
network_manage = true
`))
	if err != nil {
		t.Fatalf("a legacy fleet_size key must not break startup: %v", err)
	}
	if !c.NetworkManage {
		t.Error("NetworkManage lost while ignoring the legacy key")
	}
}

// UT-20: the production config is what actually ships; pin its contract.
func TestDeployConfigManagesTheRadio(t *testing.T) {
	c, err := config.Load("../../deploy/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.NetworkManage {
		t.Error("deploy/config.toml must manage the radio, or no unit ever elects a role")
	}
}
