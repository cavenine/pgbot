package why

import (
	"testing"
	"time"
)

func pts(vals ...float64) []Point {
	t0 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Point, len(vals))
	for i, v := range vals {
		out[i] = Point{At: t0.Add(time.Duration(i) * time.Hour), Val: v}
	}
	return out
}

// A clean step up is the canonical onset: the shift lands on the first
// point of the new regime, with before/after means reported.
func TestDetectShift_stepUp(t *testing.T) {
	s := detectShift(pts(8, 8, 9, 26, 25, 27), shiftCfg{MinRatio: 1.5})
	if s == nil {
		t.Fatal("clean 8→26 step must be detected")
	}
	if s.At != pts(0, 0, 0, 0)[3].At {
		t.Errorf("onset must be at the first shifted point, got %v", s.At)
	}
	if s.Before < 8 || s.Before > 9 || s.After < 25 || s.After > 27 {
		t.Errorf("before/after means off: %+v", s)
	}
}

// Flat and gently noisy series must not produce onsets — the detector's
// false-positive discipline is what keeps why-chains trustworthy.
func TestDetectShift_flatAndNoise(t *testing.T) {
	if s := detectShift(pts(10, 10, 10, 10, 10), shiftCfg{MinRatio: 1.5}); s != nil {
		t.Errorf("flat series produced an onset: %+v", s)
	}
	if s := detectShift(pts(10, 11, 9, 10, 12, 9), shiftCfg{MinRatio: 1.5}); s != nil {
		t.Errorf("noise produced an onset: %+v", s)
	}
}

// One spike that returns to baseline is not a sustained shift.
func TestDetectShift_spikeIsNotSustained(t *testing.T) {
	if s := detectShift(pts(10, 10, 40, 10, 10), shiftCfg{MinRatio: 1.5}); s != nil {
		t.Errorf("a single returning spike must not be an onset: %+v", s)
	}
}

// Downward shifts (cache-hit collapse) are detected with Direction=-1 and an
// absolute-delta config for ratio-type series.
func TestDetectShift_downwardAbsolute(t *testing.T) {
	s := detectShift(pts(0.99, 0.99, 0.98, 0.71, 0.70), shiftCfg{MinAbsDelta: 0.05, Direction: -1})
	if s == nil {
		t.Fatal("0.99→0.70 cache-hit collapse must be detected")
	}
	if s.After > s.Before {
		t.Error("downward shift must report After < Before")
	}
	// An upward move must NOT satisfy a downward detector.
	if s := detectShift(pts(0.70, 0.71, 0.98, 0.99), shiftCfg{MinAbsDelta: 0.05, Direction: -1}); s != nil {
		t.Errorf("upward move detected by downward detector: %+v", s)
	}
}

// A zero baseline (seq scans were 0/s, then 50/s) is the strongest shift of
// all and must not be lost to a divide-by-zero.
func TestDetectShift_zeroBaseline(t *testing.T) {
	s := detectShift(pts(0, 0, 0, 50, 52), shiftCfg{MinRatio: 1.5})
	if s == nil {
		t.Fatal("0 → 50/s must be detected")
	}
}

// Fewer than 3 points cannot establish a regime change.
func TestDetectShift_tooShort(t *testing.T) {
	if s := detectShift(pts(10, 30), shiftCfg{MinRatio: 1.5}); s != nil {
		t.Errorf("2 points must never yield an onset: %+v", s)
	}
}
