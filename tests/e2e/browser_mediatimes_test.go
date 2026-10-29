//go:build e2e && linux

package e2e

import (
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/corpus"
	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/pubsub/events"
	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/webrtc/recorder"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ntpEpoch is the NTP era's start, 1900-01-01, in Unix seconds.
const ntpEpoch = -2208988800

func ntpTime(ntp uint64) time.Time {
	return time.Unix(int64(ntp>>32)+ntpEpoch, int64((ntp&0xFFFFFFFF)*uint64(time.Second)>>32))
}

// senderClock maps a track's RTP timestamps to its sender's capture clock
// through the sender reports in its trace. LiveKit forwards them with the
// publisher's NTP time and the downtrack's RTP time.
type senderClock struct {
	rate    uint32
	reports []corpus.Record
}

func newSenderClock(tr *corpus.Trace) senderClock {
	c := senderClock{rate: tr.Header.ClockRate}
	for _, r := range tr.Records {
		if r.T == corpus.RecordSR {
			c.reports = append(c.reports, r)
		}
	}

	return c
}

// at returns when the sender captured the media at ts, by the report
// nearest to it.
func (c senderClock) at(ts uint32) time.Time {
	sr := slices.MinFunc(c.reports, func(a, b corpus.Record) int {
		return cmp.Compare(abs64(int64(int32(ts-a.TS))), abs64(int64(int32(ts-b.TS))))
	})

	return ntpTime(sr.NTP).Add(time.Duration(int32(ts-sr.TS)) * time.Second / time.Duration(c.rate))
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}

	return v
}

// firstWrittenFrame returns the timestamp and earliest read of the first
// frame the recorder writes from tr. For VP8 that is the first key frame
// read whole, by timestamp; for Opus, the first frame.
func firstWrittenFrame(tr *corpus.Trace) (uint32, int64) {
	type frame struct {
		ts   uint32
		ext  int64
		recs []corpus.Record
	}

	var frames []*frame
	index := map[uint32]*frame{}
	for _, r := range tr.Records {
		if r.T != corpus.RecordRTP || r.Pad {
			continue
		}

		f := index[r.TS]
		if f == nil {
			f = &frame{ts: r.TS}
			if len(frames) > 0 {
				f.ext = int64(int32(r.TS - frames[0].ts))
			}
			index[r.TS] = f
			frames = append(frames, f)
		}
		f.recs = append(f.recs, r)
	}

	slices.SortFunc(frames, func(a, b *frame) int { return cmp.Compare(a.ext, b.ext) })

	for _, f := range frames {
		if tr.Header.Codec == corpus.CodecVP8 && !wholeKeyFrame(f.recs) {
			continue
		}

		return f.ts, slices.MinFunc(f.recs, func(a, b corpus.Record) int { return cmp.Compare(a.AtUs, b.AtUs) }).AtUs
	}

	return 0, -1
}

// wholeKeyFrame reports whether recs, one frame's reads, hold a key frame's
// packets from its first to its marked last, none missing.
func wholeKeyFrame(recs []corpus.Record) bool {
	seqs := map[uint16]bool{}
	var first, last uint16
	var hasFirst, hasLast bool

	for _, r := range recs {
		seqs[r.Seq] = true
		if recorder.IsVP8KeyFrame(r.Packet()) {
			first, hasFirst = r.Seq, true
		}
		if r.M {
			last, hasLast = r.Seq, true
		}
	}

	if !hasFirst || !hasLast {
		return false
	}

	for s := first; s != last+1; s++ {
		if !seqs[s] {
			return false
		}
	}

	return true
}

// capturePlacement is what placeOnCaptureClock finds for one file.
type capturePlacement struct {
	Late    time.Duration // how late the first frame plays after its capture
	Worst   time.Duration // the other frames' worst drift from Late
	Started events.RecordingMediaStarted
	Out     *Output
}

// placeOnCaptureClock places each frame of rec at mediaStart plus its PTS,
// and finds its capture time through the sender reports of the browser
// track sid. It checks that mediaStart is at most 500 ms before the first
// written frame's read, and at most 1 ms after it.
func placeOnCaptureClock(ctx context.Context, t *testing.T, s *Stack, name string, rec *Recording, sid string) capturePlacement {
	t.Helper()

	out := ReadOutput(t, rec, sid)
	clock := newSenderClock(out.Trace)
	require.NotEmpty(t, clock.reports, "%s: no sender report", name)

	// The recorder sends recordingMediaStarted before recordingStopped,
	// which has already arrived, so a short wait is enough.
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	var started events.RecordingMediaStarted
	_, err := s.Driver.waitDecode(waitCtx, rec.From, events.RecordingMediaStartedKey, rec.SessionID, &started)
	waitCancel()
	require.NoError(t, err, "%s: recordingMediaStarted", name)
	start := time.UnixMilli(started.MediaStartUTC)

	ts0, readUs := firstWrittenFrame(out.Trace)
	require.GreaterOrEqual(t, readUs, int64(0), "%s: no frame the recorder would write", name)
	read := time.UnixMicro(out.Trace.Header.EpochUnixUs + readUs)
	assert.WithinRange(t, start, read.Add(-500*time.Millisecond), read.Add(time.Millisecond),
		"%s: mediaStart against the first frame's read", name)

	captured := clock.at(ts0)
	late := start.Sub(captured)

	var worst time.Duration
	for _, pts := range out.WebM.PTS {
		d := pts - out.WebM.PTS[0]
		ts := ts0 + uint32(d*int64(clock.rate)/1000)
		drift := start.Add(time.Duration(d)*time.Millisecond).Sub(clock.at(ts)) - late
		if drift.Abs() > worst.Abs() {
			worst = drift
		}
	}

	flowingAt := firstFlowing(s.Driver, rec.From, rec.SessionID)
	t.Logf("%s: %d blocks, %d sender reports; mediaStart plays the media %v after its capture, "+
		"drifting %v at worst; a start stamped on flowing would play it %v after",
		name, out.WebM.Blocks, len(clock.reports), late, worst, flowingAt.Sub(captured))

	return capturePlacement{Late: late, Worst: worst, Started: started, Out: out}
}

// TestBrowserMediaTimestampsKeepLipSync records Chromium's camera and
// microphone, one session each. Both tracks' sender reports map their RTP
// timestamps to the browser's capture clock. A file's mediaStart, less the
// capture time of its first frame, is how late it plays its media. The two
// files must agree for the recording to keep lip sync.
//
// Chromium runs on this host, so its capture clock is the recorder's wall
// clock.
func TestBrowserMediaTimestampsKeepLipSync(t *testing.T) {
	RequireBrowser(t)

	const (
		recordFor = 10 * time.Second
		// Viewers notice audio that leads video by 45 ms (ITU-R BT.1359).
		// The bound includes the encoders' own delays, which differ between
		// audio and video and are part of any WebRTC call.
		lipSyncBound = 45 * time.Millisecond
		// A file's frames follow its timestamps; against the capture clock
		// they move by the network's jitter and the sender's clock drift.
		driftBound = 30 * time.Millisecond
	)

	s := NewStack(t, StackOptions{})
	room := "e2e-browser-mediatimes-" + uuid.NewString()[:8]
	b := s.NewBrowser()
	peer := b.Join(room, "e2e-browser")
	mic := peer.PublishMic(MicOptions{})
	cam := peer.PublishCamera(CameraOptions{})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	recCam, err := s.Driver.StartRecording(ctx, room, cam.SID)
	require.NoError(t, err)
	recMic, err := s.Driver.StartRecording(ctx, room, mic.SID)
	require.NoError(t, err)

	for _, rec := range []*Recording{recCam, recMic} {
		_, err = s.Driver.WaitRtpStatus(ctx, rec.From, rec.SessionID, "flowing")
		require.NoError(t, err)
	}

	time.Sleep(recordFor)

	for _, rec := range []*Recording{recCam, recMic} {
		_, err = s.Driver.StopAndWait(ctx, rec.SessionID)
		require.NoError(t, err)
	}

	late := map[string]time.Duration{}
	for _, c := range []struct {
		name string
		rec  *Recording
		sid  string
	}{{"camera", recCam, cam.SID}, {"microphone", recMic, mic.SID}} {
		p := placeOnCaptureClock(ctx, t, s, c.name, c.rec, c.sid)
		late[c.name] = p.Late
		assert.LessOrEqual(t, p.Worst.Abs(), driftBound, "%s: drift against the capture clock", c.name)
	}

	lipSync := late["camera"] - late["microphone"]
	t.Logf("lip sync: the camera plays %v later than the microphone", lipSync)
	assert.LessOrEqual(t, lipSync.Abs(), lipSyncBound, "lip sync")
}
