// Package ffcam is the dev-mode camera adapter: it reads any webcam through
// an ffmpeg subprocess (avfoundation on macOS, v4l2 on Linux) and emits
// MJPEG frames — pure Go, no cgo, usable wherever ffmpeg runs. The
// production capture path on the Pi stays internal/camera (go4vl), which is
// the only place camera controls (focus/exposure pinning, FR-9) can be
// applied. As a hardware adapter this package may read the wall clock
// (hard rule 6).
package ffcam

import (
	"bytes"
	"io"
)

var (
	soi = []byte{0xff, 0xd8} // start of image
	eoi = []byte{0xff, 0xd9} // end of image
)

// Scanner splits a concatenated MJPEG byte stream (as ffmpeg's mjpeg muxer
// emits it: JFIF frames, no exotic APP payloads) into single JPEGs. Entropy-
// coded data byte-stuffs 0xFF, so scanning for the markers is safe here.
type Scanner struct {
	r   io.Reader
	buf []byte
}

// NewScanner wraps the ffmpeg stdout pipe.
func NewScanner(r io.Reader) *Scanner {
	return &Scanner{r: r}
}

// maxBuffered bounds the scan buffer so a corrupt stream — an SOI whose EOI
// never arrives, or endless garbage with no SOI at all — cannot grow it
// without bound (NFR-2). No sane MJPEG frame approaches this size; on
// overflow the buffered data is discarded and scanning resyncs at the next
// SOI in the stream.
const maxBuffered = 32 << 20

// Next returns the next complete JPEG (its own copy). io.EOF after the last
// complete frame; a truncated trailing frame is discarded.
func (s *Scanner) Next() ([]byte, error) {
	chunk := make([]byte, 64<<10)
	for {
		if i := bytes.Index(s.buf, soi); i >= 0 {
			s.buf = s.buf[i:] // drop garbage before the frame start
			if j := bytes.Index(s.buf[2:], eoi); j >= 0 {
				end := 2 + j + len(eoi)
				frame := bytes.Clone(s.buf[:end])
				s.buf = append(s.buf[:0:0], s.buf[end:]...) // fresh backing array
				return frame, nil
			}
			if len(s.buf) > maxBuffered {
				s.discardKeepingTail() // unterminated frame: drop and resync
			}
		} else if len(s.buf) > 1 {
			// no frame start anywhere: keep only the last byte (an SOI could
			// straddle two reads) so garbage cannot accumulate
			s.buf = append(s.buf[:0], s.buf[len(s.buf)-1:]...)
		}
		n, err := s.r.Read(chunk)
		s.buf = append(s.buf, chunk[:n]...)
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}
	}
}

// discardKeepingTail drops the buffer down to its last byte (a possible
// split marker) with a fresh small backing array, releasing the oversized
// one to the GC.
func (s *Scanner) discardKeepingTail() {
	s.buf = bytes.Clone(s.buf[len(s.buf)-1:])
}
