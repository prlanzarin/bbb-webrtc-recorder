package recorder

import (
	"slices"
	"time"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/types"
)

const (
	// The first reads can find packets that waited in the receive buffers,
	// all read at once. So until the frames written span this much media, a
	// frame that arrived sooner than its PTS implies moves the start back.
	mediaSpanSettleWindow = 500 * time.Millisecond
	// Frames read in a burst are at most a few frames apart. A longer step is
	// a timestamp jump. The frames after it would move the start back by the
	// jump, so the start settles there.
	mediaSpanMaxBacklogStep = 120 * time.Millisecond
	// The step before the last frame stands for its duration, up to these.
	// Opus packets are 120 ms at most.
	maxAudioStep = 120 * time.Millisecond
	maxVideoStep = time.Second
	// Frames whose arrivals are kept until the start settles.
	mediaSpanMaxArrivals = 256
)

// mediaSpan finds where the media a recorder writes sits on the wall clock.
// A frame arrives with its earliest packet; a retransmission can trail the
// rest. A frame that repeats a written frame's timestamp, as after an Opus
// step back, gets its own arrival. Two tracks in one file are followed as
// one, from the first frame either writes.
type mediaSpan struct {
	arrivals map[uint32]time.Time // by RTP timestamp, until the start settles
	order    []uint32             // the keys of arrivals, oldest first

	written  bool
	settled  bool
	start    time.Time
	firstPTS time.Duration
	lastPTS  time.Duration
	lastStep time.Duration
}

// arrived notes that a packet of the frame at RTP timestamp ts arrived at at.
func (s *mediaSpan) arrived(ts uint32, at time.Time) {
	if s.settled {
		return
	}

	if prev, ok := s.arrivals[ts]; ok {
		if at.Before(prev) {
			s.arrivals[ts] = at
		}

		return
	}

	if s.arrivals == nil {
		s.arrivals = make(map[uint32]time.Time)
	}

	if len(s.order) == mediaSpanMaxArrivals {
		delete(s.arrivals, s.order[0])
		s.order = s.order[1:]
	}

	s.arrivals[ts] = at
	s.order = append(s.order, ts)
}

// wrote notes that the frame at RTP timestamp ts was written at pts, step
// after the frame written before it.
func (s *mediaSpan) wrote(ts uint32, pts, step, maxStep time.Duration) {
	at, known := s.arrivals[ts]
	if known {
		delete(s.arrivals, ts)
		if i := slices.Index(s.order, ts); i >= 0 {
			s.order = slices.Delete(s.order, i, i+1)
		}
	}

	switch {
	case !s.written:
		if !known {
			at = time.Now()
		}

		s.written = true
		s.start = at
		s.firstPTS = pts
	case s.settled:
	case step > mediaSpanMaxBacklogStep || pts-s.firstPTS > mediaSpanSettleWindow:
		s.settled = true
		s.arrivals = nil
		s.order = nil
	case known:
		if start := at.Add(-(pts - s.firstPTS)); start.Before(s.start) {
			s.start = start
		}
	}

	s.lastPTS = pts
	s.lastStep = min(step, maxStep)
}

// span returns false while no frame is written. The span starts at the
// first frame written, not at PTS 0. record-and-playback's archive step
// remuxes the file, which puts that frame at time 0.
func (s *mediaSpan) span() (types.MediaSpan, bool) {
	if !s.written {
		return types.MediaSpan{}, false
	}

	return types.MediaSpan{
		Start:   s.start,
		End:     s.start.Add(s.lastPTS - s.firstPTS + s.lastStep),
		Settled: s.settled,
	}, true
}
