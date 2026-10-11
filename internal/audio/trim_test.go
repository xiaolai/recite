package audio

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// tone is d of a 440 Hz sine whose RMS is dbfs.
func tone(d time.Duration, dbfs float64) PCM {
	n := int(d * SampleRate / time.Second)
	amp := 32768 * math.Pow(10, dbfs/20) * math.Sqrt2
	p := make(PCM, 2*n)
	for i := range n {
		v := amp * math.Sin(2*math.Pi*440*float64(i)/SampleRate)
		binary.LittleEndian.PutUint16(p[2*i:], uint16(int16(math.Round(v))))
	}
	return p
}

func concat(parts ...PCM) PCM {
	var out PCM
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// accept: AUD-02#1
func TestTrimSyntheticSignals(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name string
		in   PCM
		want time.Duration
	}{
		{"leading and trailing silence", concat(Silence(500*ms), tone(time.Second, -20), Silence(300*ms)), time.Second + 2*TrimMargin},
		{"silence shorter than the margin", concat(Silence(20*ms), tone(time.Second, -20), Silence(30*ms)), time.Second + 50*ms},
		{"no silence", tone(time.Second, -20), time.Second},
		{"quiet passage at the edges is kept", concat(Silence(200*ms), tone(300*ms, -40), tone(500*ms, -10), tone(300*ms, -40), Silence(400*ms)), 1100*ms + 2*TrimMargin},
		{"quiet throughout is kept", tone(2*time.Second, -40), 2 * time.Second},
		{"quiet gap inside is kept", concat(tone(200*ms, -10), Silence(400*ms), tone(200*ms, -10)), 800 * ms},
		{"all silence", Silence(time.Second), 0},
		{"below threshold is silence", concat(Silence(100*ms), tone(300*ms, -60), Silence(100*ms)), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Trim(c.in).Duration()
			if diff := got - c.want; diff < -TrimWindow || diff > TrimWindow {
				t.Fatalf("trimmed to %v, want %v within one window", got, c.want)
			}
		})
	}
}

func TestTrimKeepsWholeSamples(t *testing.T) {
	in := append(concat(Silence(100*time.Millisecond), tone(100*time.Millisecond, -20)), 0x7f)
	if err := Trim(in).Validate(); err != nil {
		t.Fatal(err)
	}
}
