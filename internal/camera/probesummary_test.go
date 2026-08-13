package camera

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// UT-39: a Pi 5 enumerates nineteen /dev/video* nodes, seventeen of which are
// codec and ISP endpoints that could never be a camera. Reporting every one of
// them, in full, on every retry of a supervisor that retries several times a
// second, is how the one line that mattered — a camera refusing a focus value
// — ended up buried in kilobytes of identical noise. The summary keeps every
// distinct reason exactly once and leads with the rarest, because the node
// that failed differently from the crowd is the camera.
func TestSummarizeProbeErrors(t *testing.T) {
	unsupported := errors.New("open: device open: feature unsupported error")
	nodes := []string{"/dev/video0", "/dev/video1"}
	errs := []error{
		errors.New("camera: set focus_absolute=0: out-of-range failure: val 0: expected ctrl.Min 1, ctrl.Max 1023"),
		errors.New("camera: probe /dev/video1: get default format: pix format failed: bad argument error"),
	}
	for i := 19; i <= 35; i++ {
		node := fmt.Sprintf("/dev/video%d", i)
		nodes = append(nodes, node)
		errs = append(errs, fmt.Errorf("camera: probe %s: %w", node, unsupported))
	}

	got := summarizeProbeErrors(nodes, errs)

	if lines := strings.Count(got, "\n") + 1; lines > 6 {
		t.Errorf("summary is %d lines; it is logged on every retry:\n%s", lines, got)
	}
	if n := strings.Count(got, "feature unsupported"); n != 1 {
		t.Errorf("the shared failure appears %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "17") {
		t.Errorf("the summary does not say how many nodes shared that failure:\n%s", got)
	}
	// The interesting failure survives in full — it is the whole reason
	// anybody reads this.
	if !strings.Contains(got, "expected ctrl.Min 1, ctrl.Max 1023") {
		t.Errorf("the camera's own failure was summarised away:\n%s", got)
	}
	// ...and comes before the crowd.
	if strings.Index(got, "ctrl.Min") > strings.Index(got, "feature unsupported") {
		t.Errorf("the crowd is reported before the camera:\n%s", got)
	}
	if !strings.Contains(got, "19 nodes") {
		t.Errorf("the summary does not say how many nodes were tried:\n%s", got)
	}
	// Every node is still accounted for by name or by count; a reader must be
	// able to tell whether the one they care about was tried at all.
	for _, node := range []string{"/dev/video0", "/dev/video1", "/dev/video19", "/dev/video35"} {
		if !strings.Contains(got, node) {
			t.Errorf("node %s is not mentioned anywhere:\n%s", node, got)
		}
	}
}

// UT-39: one node, one reason — no counting, no grouping, nothing that reads
// like a summary of a crowd that isn't there.
func TestSummarizeProbeErrorsSingleNode(t *testing.T) {
	got := summarizeProbeErrors(
		[]string{"/dev/video0"},
		[]error{errors.New("camera: probe /dev/video0: feature unsupported error")},
	)
	if strings.Contains(got, "\n") {
		t.Errorf("a single failure should be one line:\n%s", got)
	}
	if !strings.Contains(got, "/dev/video0") || !strings.Contains(got, "feature unsupported") {
		t.Errorf("the one failure is not reported plainly: %s", got)
	}
}
