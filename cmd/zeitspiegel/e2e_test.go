//go:build integration

package main_test

// ST-1-style API contract suite against the real binary in synth mode (the
// CI hardware lane swaps the source for a v4l2loopback device; the contract
// below is source-agnostic). Needs ffmpeg/ffprobe → integration tag.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startBinary builds and launches zeitspiegel --source synth, waits for
// /healthz, and returns the base URL plus the process.
func startBinary(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "zeitspiegel")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command(bin, "--source", "synth", "--bind", fmt.Sprintf("127.0.0.1:%d", port))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, cmd
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("binary not healthy within 15s (last err: %v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestBinaryAPIContract(t *testing.T) {
	base, cmd := startBinary(t)

	t.Run("status schema (FR-8)", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"delay_s", "fps", "resolution", "buffer", "dropped_frames", "min_latency_ms", "warming_up", "uptime_s"} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing %q", k)
			}
		}
		if fps := m["fps"].(float64); fps != 60 {
			t.Errorf("fps = %v, want 60 (default profile)", fps)
		}
	})

	t.Run("delay roundtrip (FR-3)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, base+"/api/v1/delay", bytes.NewReader([]byte(`{"seconds": 2.0}`)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("PUT delay: %d", resp.StatusCode)
		}
		st, err := http.Get(base + "/api/v1/status")
		if err != nil {
			t.Fatal(err)
		}
		defer st.Body.Close()
		var m struct {
			DelayS float64 `json:"delay_s"`
		}
		json.NewDecoder(st.Body).Decode(&m)
		if m.DelayS != 2.0 {
			t.Errorf("delay_s = %v, want 2.0", m.DelayS)
		}
	})

	t.Run("validation (FR-11)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, base+"/api/v1/delay", bytes.NewReader([]byte(`{"seconds": -1}`)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 422 {
			t.Errorf("negative delay: %d, want 422", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("content-type = %q", ct)
		}
		r2, _ := http.Get(base + "/api/v1/clip?seconds=0")
		r2.Body.Close()
		if r2.StatusCode != 422 {
			t.Errorf("clip seconds=0: %d, want 422", r2.StatusCode)
		}
	})

	t.Run("clip after warm-up (FR-5)", func(t *testing.T) {
		time.Sleep(3 * time.Second) // let synth fill ~3 s of buffer
		resp, err := http.Get(base + "/api/v1/clip?seconds=2")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("clip: %d (%s)", resp.StatusCode, b)
		}
		if xd := resp.Header.Get("X-Clip-Duration"); xd == "" {
			t.Error("missing X-Clip-Duration")
		}
		clip := filepath.Join(t.TempDir(), "clip.mp4")
		f, _ := os.Create(clip)
		io.Copy(f, resp.Body)
		f.Close()
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
			"stream=codec_name", "-of", "default=nw=1:nk=1", clip).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != "h264" {
			t.Errorf("codec = %q, want h264", got)
		}
	})

	t.Run("config roundtrip (FR-9)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPatch, base+"/api/v1/config", bytes.NewReader([]byte(`{"mirror_flip": false}`)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("PATCH config: %d", resp.StatusCode)
		}
		var rt struct {
			MirrorFlip bool `json:"mirror_flip"`
		}
		json.NewDecoder(resp.Body).Decode(&rt)
		if rt.MirrorFlip {
			t.Error("mirror_flip still true after patch")
		}
	})

	t.Run("web ui served (FR-7)", func(t *testing.T) {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET /: %d", resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(strings.ToLower(string(b)), "zeitspiegel") {
			t.Error("UI page does not mention zeitspiegel")
		}
	})

	t.Run("clean shutdown on SIGTERM", func(t *testing.T) {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("exit: %v, want clean 0", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("did not exit within 5s of SIGTERM")
		}
	})
}
