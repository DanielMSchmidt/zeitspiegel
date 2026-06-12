package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// JPEG APP4 tagging: the synthetic source embeds (seq, capture timestamp)
// into each frame so tests can assert frame accuracy end to end
// (ARCHITECTURE §D6). Segment payload: "ZSPG" + 8-byte seq + 8-byte unix
// nanoseconds, big endian.

var (
	app4Magic = []byte("ZSPG")

	// ErrNoTag is returned by ParseAPP4 when the JPEG carries no ZSPG APP4 segment.
	ErrNoTag = errors.New("frame: no ZSPG APP4 segment")
)

const (
	markerSOI  = 0xd8
	markerSOS  = 0xda
	markerAPP4 = 0xe4
)

// TagAPP4 returns a copy of jpg with a ZSPG APP4 segment inserted directly
// after SOI. The input is not modified (Frame.JPEG is immutable).
func TagAPP4(jpg []byte, seq uint64, ts time.Time) ([]byte, error) {
	if len(jpg) < 2 || jpg[0] != 0xff || jpg[1] != markerSOI {
		return nil, errors.New("frame: not a JPEG (missing SOI)")
	}
	payload := make([]byte, 0, len(app4Magic)+16)
	payload = append(payload, app4Magic...)
	payload = binary.BigEndian.AppendUint64(payload, seq)
	payload = binary.BigEndian.AppendUint64(payload, uint64(ts.UnixNano()))

	seg := make([]byte, 0, 4+len(payload))
	seg = append(seg, 0xff, markerAPP4)
	seg = binary.BigEndian.AppendUint16(seg, uint16(2+len(payload)))
	seg = append(seg, payload...)

	out := make([]byte, 0, len(jpg)+len(seg))
	out = append(out, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, jpg[2:]...)
	return out, nil
}

// ParseAPP4 extracts (seq, capture timestamp) from a ZSPG APP4 segment.
func ParseAPP4(jpg []byte) (seq uint64, ts time.Time, err error) {
	if len(jpg) < 2 || jpg[0] != 0xff || jpg[1] != markerSOI {
		return 0, time.Time{}, errors.New("frame: not a JPEG (missing SOI)")
	}
	i := 2
	for i+4 <= len(jpg) && jpg[i] == 0xff {
		marker := jpg[i+1]
		if marker == markerSOS { // entropy-coded data follows; no tag found
			break
		}
		segLen := int(binary.BigEndian.Uint16(jpg[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(jpg) {
			return 0, time.Time{}, fmt.Errorf("frame: corrupt segment at offset %d", i)
		}
		payload := jpg[i+4 : i+2+segLen]
		if marker == markerAPP4 && len(payload) == len(app4Magic)+16 &&
			string(payload[:len(app4Magic)]) == string(app4Magic) {
			seq = binary.BigEndian.Uint64(payload[4:12])
			ns := binary.BigEndian.Uint64(payload[12:20])
			return seq, time.Unix(0, int64(ns)).UTC(), nil
		}
		i += 2 + segLen
	}
	return 0, time.Time{}, ErrNoTag
}
