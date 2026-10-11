package audio

import (
	"encoding/binary"
	"math"
	"time"
)

// Silence trimming parameters (AUD-02).
const (
	TrimWindow    = 10 * time.Millisecond
	TrimThreshold = -50.0 // dBFS, RMS over one window
	TrimMargin    = 40 * time.Millisecond
)

const (
	trimWindowBytes = int(TrimWindow * BytesPerSecond / time.Second)
	trimMarginBytes = int(TrimMargin * BytesPerSecond / time.Second)
)

// trimThresholdSq is the squared RMS amplitude of TrimThreshold on the 16-bit scale.
var trimThresholdSq = math.Pow(32768*math.Pow(10, TrimThreshold/20), 2)

// Trim removes leading and trailing silence: every 10 ms window whose RMS is below −50 dBFS
// before the first louder window and after the last one, keeping a 40 ms margin on each side.
// Audio that is silent throughout trims to nothing. The result shares p's backing array. A
// trailing partial sample is ignored.
func Trim(p PCM) PCM {
	p = p[:len(p)-len(p)%BytesPerSample]
	first, last := -1, -1
	for off := 0; off < len(p); off += trimWindowBytes {
		end := min(off+trimWindowBytes, len(p))
		if loud(p[off:end]) {
			if first < 0 {
				first = off
			}
			last = end
		}
	}
	if first < 0 {
		return p[:0]
	}
	return p[max(0, first-trimMarginBytes):min(len(p), last+trimMarginBytes)]
}

// loud reports whether the RMS of w reaches the trim threshold.
func loud(w PCM) bool {
	n := len(w) / BytesPerSample
	if n == 0 {
		return false
	}
	var sum float64
	for i := 0; i < len(w); i += BytesPerSample {
		v := float64(int16(binary.LittleEndian.Uint16(w[i:])))
		sum += v * v
	}
	return sum/float64(n) >= trimThresholdSq
}

// Silence returns d of digital silence.
func Silence(d time.Duration) PCM {
	n := int(d * BytesPerSecond / time.Second)
	return make(PCM, n-n%BytesPerSample)
}
