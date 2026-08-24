// Package why computes deterministic root-cause chains from baseline history:
// per-object time series over the stored snapshots, sustained-shift (onset)
// detection on each, and explicit mechanism rules that connect a symptom's
// onset to the mechanism and antecedent that precede it. No model generates
// anything here — same contract as the findings engine.
package why

import "time"

// Point is one sample of a series: a snapshot's value (or the rate between two
// adjacent snapshots, stamped with the later time).
type Point struct {
	At  time.Time
	Val float64
}

// Shift is a sustained regime change: the onset time (first point of the new
// regime) and the mean value on each side.
type Shift struct {
	At     time.Time
	Before float64
	After  float64
}

// shiftCfg tunes detection per series type. Exactly one of MinRatio (for
// magnitude series: ms, scans/s) or MinAbsDelta (for bounded ratios: cache
// hit) applies. Direction: +1 up (default), -1 down.
type shiftCfg struct {
	MinRatio    float64
	MinAbsDelta float64
	Direction   int
}

// detectShift finds the sustained shift with the largest magnitude, or nil.
// Deterministic and deliberately strict: every point of the new regime must
// sit past the midpoint between the two means (a returning spike is not a
// regime), and the means must differ by the configured ratio or delta. Needs
// at least 3 points — one point on a side is allowed only for the before-side,
// so a fresh regression at the last two snapshots is still catchable.
func detectShift(points []Point, cfg shiftCfg) *Shift {
	n := len(points)
	if n < 3 {
		return nil
	}
	dir := cfg.Direction
	if dir == 0 {
		dir = 1
	}
	var best *Shift
	var bestMag float64
	for i := 1; i <= n-2; i++ { // after-side keeps >= 2 points: "sustained"
		before := mean(points[:i])
		after := mean(points[i:])
		delta := (after - before) * float64(dir)
		if delta <= 0 {
			continue
		}
		// Threshold: ratio for magnitude series (zero baseline always passes —
		// 0 → anything is the strongest shift there is), absolute for ratios.
		switch {
		case cfg.MinRatio > 0:
			if before > 0 && after/before < cfg.MinRatio && dir > 0 {
				continue
			}
			if dir < 0 && after > 0 && before/after < cfg.MinRatio {
				continue
			}
			if before == 0 && after == 0 {
				continue
			}
		case cfg.MinAbsDelta > 0:
			if delta < cfg.MinAbsDelta {
				continue
			}
		default:
			continue
		}
		// Sustained: every after-point sits past the midpoint of the two means.
		mid := (before + after) / 2
		sustained := true
		for _, p := range points[i:] {
			if (p.Val-mid)*float64(dir) < 0 {
				sustained = false
				break
			}
		}
		if !sustained {
			continue
		}
		if best == nil || delta > bestMag {
			bestMag = delta
			best = &Shift{At: points[i].At, Before: before, After: after}
		}
	}
	return best
}

func mean(points []Point) float64 {
	if len(points) == 0 {
		return 0
	}
	var s float64
	for _, p := range points {
		s += p.Val
	}
	return s / float64(len(points))
}
