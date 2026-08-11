package screen

// fitRect returns the largest rectangle with the source's aspect ratio that
// fits inside dst, centred, as (x, y, w, h). The mirror must not distort what
// it shows — a dancer checking body line against a stretched image is reading
// a lie — so the renderer letterboxes rather than filling (FR-16).
//
// Comparison and division stay in integers: no float rounding, and the fitted
// edge lands on an exact pixel. A non-positive source (a frame that failed to
// decode a sensible geometry) falls back to filling dst, which is the
// behaviour that predates this function.
func fitRect(srcW, srcH, dstW, dstH int) (x, y, w, h int) {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return 0, 0, dstW, dstH
	}
	if srcW*dstH > dstW*srcH {
		// Source is proportionally wider than dst: pin width, bars top/bottom.
		w, h = dstW, srcH*dstW/srcW
	} else {
		// Source is proportionally taller (or equal): pin height, bars left/right.
		w, h = srcW*dstH/srcH, dstH
	}
	return (dstW - w) / 2, (dstH - h) / 2, w, h
}
