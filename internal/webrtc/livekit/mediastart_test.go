package livekit

import (
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/corpus"
	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/webrtc/recorder"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A packet the buffer holds behind a loss keeps the time it was pushed.
func TestTrackBufferReleasesPacketsWithTheirArrival(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newTrackBuffer(&codecs.OpusPacket{}, testLatency, lossKeyframeInterval, func() {})
		defer b.close()

		t0 := time.Now()
		assert.Len(t, b.pushExt(opusPacket(100), t0), 1)
		assert.Empty(t, b.pushExt(opusPacket(102), t0.Add(30*time.Millisecond)))

		released := b.pushExt(opusPacket(101), t0.Add(50*time.Millisecond))
		require.Len(t, released, 2)
		assert.Equal(t, uint16(101), released[0].SequenceNumber)
		assert.Equal(t, t0.Add(50*time.Millisecond), released[0].ReceivedAt)
		assert.Equal(t, uint16(102), released[1].SequenceNumber)
		assert.Equal(t, t0.Add(30*time.Millisecond), released[1].ReceivedAt)
	})
}

func TestReadTrackPushesEachPacketWithItsArrival(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := newPushRecorder()
		h := newLoopHarness(rec, audioInfo.id)
		reads := audioReads(0, 100, 50)
		// 110 comes in after 111, 15 ms later.
		reads[10], reads[11] = reads[11], reads[10]
		reads[11].at = reads[10].at + 15*time.Millisecond

		src := newFakeTrack(reads)
		h.run(src, audioInfo)

		require.Equal(t, seqRange(100, 50), rec.audio)
		for _, r := range reads {
			assert.Equal(t, src.start.Add(r.at), rec.arrivals[r.pkt.SequenceNumber], "seq %d", r.pkt.SequenceNumber)
		}
	})
}

// mediaStartHarness is a loopHarness over a real WebmRecorder that keeps
// what the media start callback and the flow callback reported, in order.
type mediaStartHarness struct {
	*loopHarness

	mu     sync.Mutex
	events []string
	starts []time.Time
}

func newMediaStartHarness(t *testing.T, info trackInfo) *mediaStartHarness {
	rec := recorder.NewWebmRecorder(filepath.Join(t.TempDir(), "track.webm"), 0o600, 256, 32, true, false)
	rec.SetHasAudio(info.kind == TrackKindAudio)
	rec.SetHasVideo(info.kind == TrackKindVideo)

	h := &mediaStartHarness{loopHarness: newLoopHarness(rec, info.id)}
	h.w.flowCallback = func(isFlowing bool, _ time.Duration, _ bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if isFlowing {
			h.events = append(h.events, "flowing")
		}
	}
	h.w.mediaStartCallback = func(start time.Time) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, "mediaStart")
		h.starts = append(h.starts, start)
	}

	return h
}

// reported closes the recorder, whose writer runs goroutines in the bubble,
// and waits for the events still being delivered.
func (h *mediaStartHarness) reported() ([]string, []time.Time) {
	h.w.rec.Close()
	synctest.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.events...), append([]time.Time(nil), h.starts...)
}

// The media start goes out once the recorder settles it, after the flowing
// event the first packets made.
func TestAdapterReportsTheMediaStartOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMediaStartHarness(t, audioInfo)
		src := newFakeTrack(audioReads(300*time.Millisecond, 100, 100))

		h.run(src, audioInfo)

		events, starts := h.reported()
		assert.Equal(t, []string{"flowing", "mediaStart"}, events)
		require.Len(t, starts, 1)
		assert.Equal(t, src.start.Add(300*time.Millisecond), starts[0])
	})
}

// A recording too short to settle its start reports none: the stop carries
// the media span.
func TestAdapterReportsNoUnsettledMediaStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMediaStartHarness(t, audioInfo)
		src := newFakeTrack(audioReads(0, 100, 10))

		h.run(src, audioInfo)

		events, _ := h.reported()
		assert.Equal(t, []string{"flowing"}, events)
	})
}

// The first read finds five frames waiting, all read at once: the start goes
// back to when the first of them would have arrived.
func TestAdapterTakesOffAStartBacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMediaStartHarness(t, audioInfo)
		reads := audioReads(320*time.Millisecond, 100, 100)
		for i := range 5 {
			reads[i].at = reads[4].at
		}

		src := newFakeTrack(reads)
		h.run(src, audioInfo)

		_, starts := h.reported()
		require.Len(t, starts, 1)
		assert.Equal(t, src.start.Add(320*time.Millisecond), starts[0])
	})
}

// keyFrameReads is a 5 fps camera from offset at: a key frame, then delta
// frames, of three packets each, read together. The 200 ms step between
// frames settles the media start at the second frame, so only the key
// frame's arrival can time it.
func keyFrameReads(at time.Duration, frames int) []trackRead {
	var reads []trackRead
	seq := uint16(1000)
	for f := range frames {
		for j := range 3 {
			payload := append(corpus.VP8Payload(uint16(f+1), j == 0, f == 0, 640, 480), make([]byte, 64)...)
			reads = append(reads, trackRead{
				at: at + time.Duration(f)*time.Second/5,
				pkt: &rtp.Packet{
					Header:  rtp.Header{SequenceNumber: seq, Timestamp: uint32(f * 18000), Marker: j == 2},
					Payload: payload,
				},
			})
			seq++
		}
	}

	return reads
}

// The key frame's middle packet comes in 80 ms after the rest. The buffer
// holds the frame until then, but it arrived with its first packet.
func TestAdapterTimesAHeldKeyFrameByItsFirstPacket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMediaStartHarness(t, videoInfo)
		reads := keyFrameReads(300*time.Millisecond, 30)
		late := reads[1]
		late.at += 80 * time.Millisecond
		reads = append(append([]trackRead{reads[0], reads[2]}, late), reads[3:]...)

		src := newFakeTrack(reads)
		h.run(src, videoInfo)

		_, starts := h.reported()
		require.Len(t, starts, 1)
		assert.Equal(t, src.start.Add(300*time.Millisecond), starts[0])
	})
}

// A microphone that mutes 200 ms in writes no frame to settle its start:
// the start goes out once no frame written can move it.
func TestAdapterReportsTheMediaStartOfAPausedTrack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMediaStartHarness(t, audioInfo)
		src := newFakeTrack(audioReads(300*time.Millisecond, 100, 10), audioReads(10*time.Second, 110, 5))

		h.run(src, audioInfo)

		events, starts := h.reported()
		assert.Equal(t, []string{"flowing", "mediaStart", "flowing"}, events)
		require.Len(t, starts, 1)
		assert.Equal(t, src.start.Add(300*time.Millisecond), starts[0])
	})
}
