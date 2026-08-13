package camera

import (
	"fmt"
	"sort"
	"strings"
)

// summarizeProbeErrors turns one failure per /dev/video* node into something a
// human reads at three in the morning.
//
// A Pi 5 enumerates nineteen video nodes and seventeen of them are codec and
// ISP endpoints that could never be a camera. The capture supervisor retries
// several times a second, so reporting all nineteen in full meant kilobytes of
// identical text per second, with the one line that mattered — a camera
// refusing a focus value — somewhere inside it.
//
// Failures are grouped by reason and the rarest is reported first, because the
// node that failed differently from the crowd is the one that is actually a
// camera. Every node is still accounted for, by name or by count: a reader has
// to be able to tell whether the device they care about was even tried.
func summarizeProbeErrors(nodes []string, errs []error) string {
	if len(nodes) == 1 && len(errs) == 1 {
		return fmt.Sprintf("%s: %s", nodes[0], trimReason(errs[0].Error(), nodes[0]))
	}

	type group struct {
		reason string
		nodes  []string
		first  int // input order, to keep ties stable
	}
	var groups []*group
	index := map[string]*group{}
	for i, node := range nodes {
		if i >= len(errs) {
			break
		}
		reason := trimReason(errs[i].Error(), node)
		g, ok := index[reason]
		if !ok {
			g = &group{reason: reason, first: i}
			index[reason] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, node)
	}

	// Rarest first: a lone failure among many identical ones is the camera
	// telling you something the codec nodes cannot.
	sort.SliceStable(groups, func(i, j int) bool {
		if len(groups[i].nodes) != len(groups[j].nodes) {
			return len(groups[i].nodes) < len(groups[j].nodes)
		}
		return groups[i].first < groups[j].first
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%d nodes tried, none usable", len(nodes))
	for _, g := range groups {
		fmt.Fprintf(&b, "\n  %s: %s", describeNodes(g.nodes), g.reason)
	}
	return b.String()
}

// describeNodes names a group: every node when there are few, and a count with
// the range when there are many. Nobody needs seventeen device paths, but they
// do need to know which seventeen.
func describeNodes(nodes []string) string {
	switch {
	case len(nodes) <= 3:
		return strings.Join(nodes, ", ")
	default:
		return fmt.Sprintf("%d nodes (%s … %s)", len(nodes), nodes[0], nodes[len(nodes)-1])
	}
}

// trimReason strips the wrapping this package added on the way out — the node
// path repeated two or three times, and the package prefix — leaving the part
// that says what actually went wrong.
func trimReason(msg, node string) string {
	msg = strings.TrimPrefix(msg, "camera: ")
	msg = strings.TrimPrefix(msg, "probe "+node+": ")
	msg = strings.ReplaceAll(msg, node+": ", "")
	return msg
}
