package recorder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const audioTicks = opusSampleRate / 50 // a 20 ms Opus frame

// spanFrame is a frame written at pts, with the arrival of its packets.
type spanFrame struct {
	ts       uint32
	pts      time.Duration
	arrivals []time.Duration // since t0
}

func runSpan(t0 time.Time, maxStep time.Duration, frames []spanFrame) *mediaSpan {
	var s mediaSpan
	var prev time.Duration

	for i, f := range frames {
		for _, a := range f.arrivals {
			s.arrived(f.ts, t0.Add(a))
		}

		step := f.pts - prev
		if i == 0 {
			step = 0
		}
		prev = f.pts

		s.wrote(f.ts, f.pts, step, maxStep)
	}

	return &s
}

// audioFrames are n 20 ms frames from pts 0, frame k read at at(k).
func audioFrames(n int, at func(k int) time.Duration) []spanFrame {
	frames := make([]spanFrame, n)
	for k := range frames {
		frames[k] = spanFrame{
			ts:       uint32(1000 + k*audioTicks),
			pts:      time.Duration(k) * 20 * time.Millisecond,
			arrivals: []time.Duration{at(k)},
		}
	}

	return frames
}

func TestMediaSpanIsEmptyUntilAFrameIsWritten(t *testing.T) {
	var s mediaSpan
	s.arrived(1000, time.Now())

	_, ok := s.span()
	assert.False(t, ok)
}

// A frame's packets can arrive out of order, e.g. its first one repaired by
// a retransmission: the frame arrived with its earliest packet.
func TestMediaSpanStartsAtTheFirstFramesEarliestPacket(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, time.Second, []spanFrame{
		{ts: 90000, pts: 0, arrivals: []time.Duration{40 * time.Millisecond, 10 * time.Millisecond, 12 * time.Millisecond}},
	})

	span, ok := s.span()
	require.True(t, ok)
	assert.Equal(t, t0.Add(10*time.Millisecond), span.Start)
	assert.False(t, span.Settled)
}

// Packets of frames dropped before the first written one (a camera waiting
// for its key frame) do not move the start.
func TestMediaSpanIgnoresFramesNotWritten(t *testing.T) {
	t0 := time.Now()
	var s mediaSpan

	for k := range 30 {
		s.arrived(uint32(k*3000), t0.Add(time.Duration(k)*33*time.Millisecond))
	}

	s.arrived(30*3000, t0.Add(time.Second))
	s.wrote(30*3000, 0, 0, time.Second)

	span, ok := s.span()
	require.True(t, ok)
	assert.Equal(t, t0.Add(time.Second), span.Start)
}

// The first read finds five frames waiting, sent 20 ms apart: the start goes
// back to when the first of them would have arrived.
func TestMediaSpanTakesOffAStartBacklog(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, maxAudioStep, audioFrames(50, func(k int) time.Duration {
		if k < 5 {
			return 80 * time.Millisecond
		}

		return time.Duration(k) * 20 * time.Millisecond
	}))

	span, ok := s.span()
	require.True(t, ok)
	assert.Equal(t, t0, span.Start)
	assert.True(t, span.Settled)
}

// A later frame that met less network delay than the first one shows the
// first was late.
func TestMediaSpanTakesTheLeastDelayedFrame(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, maxAudioStep, audioFrames(50, func(k int) time.Duration {
		if k == 0 {
			return 30 * time.Millisecond
		}

		return time.Duration(k)*20*time.Millisecond + 5*time.Millisecond
	}))

	span, _ := s.span()
	assert.Equal(t, t0.Add(5*time.Millisecond), span.Start)
}

// Frames past the settle window keep the start, however early they are.
func TestMediaSpanSettlesAfterItsWindow(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, maxAudioStep, audioFrames(100, func(k int) time.Duration {
		if k*20 > int(mediaSpanSettleWindow/time.Millisecond) {
			return time.Duration(k)*20*time.Millisecond - 50*time.Millisecond
		}

		return time.Duration(k) * 20 * time.Millisecond
	}))

	span, _ := s.span()
	assert.Equal(t, t0, span.Start)
	assert.True(t, span.Settled)
}

// A 300 ms timestamp jump inside the settle window.
func TestMediaSpanSettlesAtATimestampJump(t *testing.T) {
	t0 := time.Now()
	frames := audioFrames(5, func(k int) time.Duration { return time.Duration(k) * 20 * time.Millisecond })

	for k := range 20 {
		frames = append(frames, spanFrame{
			ts:       uint32(1000 + (5+k)*audioTicks + 300*48),
			pts:      time.Duration(5+k)*20*time.Millisecond + 300*time.Millisecond,
			arrivals: []time.Duration{time.Duration(5+k) * 20 * time.Millisecond},
		})
	}

	span, _ := runSpan(t0, maxAudioStep, frames).span()
	assert.Equal(t, t0, span.Start)
	assert.True(t, span.Settled)
}

func TestMediaSpanEndsAtTheEndOfTheLastFrame(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, maxAudioStep, audioFrames(50, func(k int) time.Duration {
		return time.Duration(k) * 20 * time.Millisecond
	}))

	span, _ := s.span()
	assert.Equal(t, t0.Add(50*20*time.Millisecond), span.End)
}

// A frame after a 5 s pause does not last 5 s.
func TestMediaSpanCapsTheLastFramesDuration(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, time.Second, []spanFrame{
		{ts: 0, pts: 0, arrivals: []time.Duration{0}},
		{ts: 3000, pts: 33 * time.Millisecond, arrivals: []time.Duration{33 * time.Millisecond}},
		{ts: 3000 + 5*90000, pts: 5033 * time.Millisecond, arrivals: []time.Duration{5033 * time.Millisecond}},
	})

	span, _ := s.span()
	assert.Equal(t, t0.Add(6033*time.Millisecond), span.End)
}

func TestMediaSpanIsRelativeToTheFirstFrameWritten(t *testing.T) {
	t0 := time.Now()
	s := runSpan(t0, time.Second, []spanFrame{
		{ts: 9000, pts: 100 * time.Millisecond, arrivals: []time.Duration{0}},
		{ts: 12000, pts: 133 * time.Millisecond, arrivals: []time.Duration{33 * time.Millisecond}},
	})

	span, _ := s.span()
	assert.Equal(t, t0, span.Start)
	assert.Equal(t, t0.Add(66*time.Millisecond), span.End)
}
