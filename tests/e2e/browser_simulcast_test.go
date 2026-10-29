//go:build e2e && linux

package e2e

import (
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/corpus"
	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/pubsub/events"
	"github.com/google/uuid"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBrowserSimulcastMediaTimestamps records Chromium's camera and
// microphone, published as BBB's client publishes them. The room has BBB's
// options, and the camera is VP8 simulcast with the default profile's
// layers.
//
// Midway, the page stops sending h for a while (SetCameraLayer). LiveKit
// then forwards q, until it takes h back. Across both switches, the
// camera's file must follow the capture clock and keep lip sync with the
// microphone's, as in TestBrowserMediaTimestampsKeepLipSync.
//
// bbb-webrtc-sfu starts recording a camera when it is published, or later,
// when recording turns on. If nobody subscribes to a camera, dynacast
// pauses all its layers. In the second case the recorder's subscription
// resumes them.
func TestBrowserSimulcastMediaTimestamps(t *testing.T) {
	RequireBrowser(t)

	t.Run("recorded at publish", func(t *testing.T) { testBrowserSimulcast(t, false) })
	t.Run("recorded after dynacast paused it", func(t *testing.T) { testBrowserSimulcast(t, true) })
}

// testBrowserSimulcast starts recording once the camera is published, or,
// with paused, once dynacast has paused its layers.
func testBrowserSimulcast(t *testing.T, paused bool) {
	const (
		// Dynacast pauses an unwatched camera 10 s after it is published.
		pauseWait = 20 * time.Second
		// Before h stops, and for how long.
		beforeDrop = 3 * time.Second
		dropFor    = 3 * time.Second
		// As in TestSimulcastMediaTimestampsPlaceEachFrame.
		upSwitchWait = 20 * time.Second
		afterUp      = 2 * time.Second
		// As in TestBrowserMediaTimestampsKeepLipSync.
		lipSyncBound = 45 * time.Millisecond
		driftBound   = 30 * time.Millisecond
	)

	s := NewStack(t, StackOptions{})
	room := "e2e-browser-simulcast-" + uuid.NewString()[:8]
	b := s.NewBrowser()
	peer := b.JoinAsBBB(room, "e2e-browser")
	mic := peer.PublishMic(MicOptions{})
	cam := peer.PublishBBBCamera("camera", bbbDefaultCameraProfile)
	t.Logf("camera: Chromium captures %dx%d at %v fps", cam.Capture.Width, cam.Capture.Height, cam.Capture.FrameRate)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	assert.Equal(t, layerSizes(bbbCameraLayers), layerSizes(listedVP8Layers(ctx, t, s.LiveKit, room, cam.SID)),
		"camera: the layers LiveKit lists")

	// The waits poll here, not in require.Eventually's goroutine: a browser
	// call that fails must stop the test from the test's own goroutine.
	if paused {
		want := map[string]bool{"q": false, "h": false}
		deadline := time.Now().Add(pauseWait)
		for l := peer.CameraLayers(cam.SID); !maps.Equal(l, want); l = peer.CameraLayers(cam.SID) {
			require.True(t, time.Now().Before(deadline), "camera: dynacast did not pause the layers: %v", l)
			time.Sleep(250 * time.Millisecond)
		}
	}

	recCam, err := s.Driver.StartRecording(ctx, room, cam.SID)
	require.NoError(t, err)
	recMic, err := s.Driver.StartRecording(ctx, room, mic.SID)
	require.NoError(t, err)

	for _, rec := range []*Recording{recCam, recMic} {
		_, err = s.Driver.WaitRtpStatus(ctx, rec.From, rec.SessionID, "flowing")
		require.NoError(t, err)
	}

	time.Sleep(beforeDrop)
	peer.SetCameraLayer(cam.SID, "h", false)
	dropped := time.Now()
	time.Sleep(dropFor)
	plis, ok := peer.CameraPLIs(cam.SID)["h"]
	require.True(t, ok, "camera: no outbound-rtp stats for h")
	peer.SetCameraLayer(cam.SID, "h", true)
	restored := time.Now()

	deadline := restored.Add(upSwitchWait)
	for p := peer.CameraPLIs(cam.SID); p["h"] <= plis; p = peer.CameraPLIs(cam.SID) {
		require.True(t, time.Now().Before(deadline), "LiveKit did not take h back: PLIs %v, h had %d before", p, plis)
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(afterUp)

	stops := map[string]*events.RecordingStopped{}
	for _, rec := range []*Recording{recCam, recMic} {
		stops[rec.SessionID], err = s.Driver.StopAndWait(ctx, rec.SessionID)
		require.NoError(t, err)
	}

	late := map[string]time.Duration{}
	var camOut *Output
	for _, c := range []struct {
		name    string
		rec     *Recording
		sid     string
		maxStep time.Duration
	}{{"camera", recCam, cam.SID, maxVideoStep}, {"microphone", recMic, mic.SID, maxAudioStep}} {
		p := placeOnCaptureClock(ctx, t, s, c.name, c.rec, c.sid)
		late[c.name] = p.Late
		assert.LessOrEqual(t, p.Worst.Abs(), driftBound, "%s: drift against the capture clock", c.name)
		assertMediaEnd(t, c.name, p.Started, stops[c.rec.SessionID], p.Out.WebM.PTS, c.maxStep)
		if c.name == "camera" {
			camOut = p.Out
		}
	}

	lipSync := late["camera"] - late["microphone"]
	t.Logf("lip sync: the camera plays %v later than the microphone", lipSync)
	assert.LessOrEqual(t, lipSync.Abs(), lipSyncBound, "lip sync")

	// Key frames tell the layers apart by size. Chromium can scale q down
	// while it is the only layer sent, so any size but h's is q. Both
	// switches must show, as in TestSimulcastMediaTimestampsPlaceEachFrame.
	h := fmt.Sprintf("%dx%d", bbbCameraLayers[1].Width, bbbCameraLayers[1].Height)
	var down, up bool
	prev := ""
	for _, kf := range keyFrameSizes(camOut.Trace) {
		t.Logf("camera: %s key frame read %v after h stopped", kf.size, kf.at.Sub(dropped))
		down = down || (prev == h && kf.size != h && kf.at.After(dropped) && kf.at.Before(restored))
		up = up || (down && prev != h && kf.size == h && kf.at.After(restored))
		prev = kf.size
	}

	assert.True(t, down, "camera: no switch from h to q while h was stopped")
	assert.True(t, up, "camera: no switch from q back to h")

	// On a switch up, LiveKit can forward one captured frame twice, q's key
	// frame and then h's, at the same PTS. That comes from LiveKit, so this
	// test only logs it, where other scenarios fail on a repeated PTS.
	t.Logf("camera: %d blocks repeat an earlier block's PTS", len(camOut.WebM.DuplicatePTS))
}

// listedVP8Layers waits until LiveKit lists the VP8 layers of the track sid
// in room, and returns them.
func listedVP8Layers(ctx context.Context, t *testing.T, lk *LiveKit, room, sid string) []*livekit.VideoLayer {
	t.Helper()

	rs := lksdk.NewRoomServiceClient(lk.HTTPURL(), lk.APIKey, lk.APISecret)

	var layers []*livekit.VideoLayer
	require.Eventually(t, func() bool {
		resp, err := rs.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
		if err != nil {
			return false
		}

		for _, pi := range resp.Participants {
			for _, ti := range pi.Tracks {
				for _, c := range ti.Codecs {
					if ti.Sid == sid && strings.EqualFold(c.MimeType, webrtc.MimeTypeVP8) && len(c.Layers) > 0 {
						layers = c.Layers
						return true
					}
				}
			}
		}

		return false
	}, 10*time.Second, 50*time.Millisecond, "LiveKit does not list the VP8 layers of %s", sid)

	return layers
}

// layerSizes describes layers as quality, size and bitrate, the fields a
// publisher sets.
func layerSizes(layers []*livekit.VideoLayer) []string {
	out := make([]string, 0, len(layers))
	for _, l := range layers {
		out = append(out, fmt.Sprintf("%s %dx%d %d bps", l.Quality, l.Width, l.Height, l.Bitrate))
	}

	return out
}

type keyFrameRead struct {
	size string
	at   time.Time
}

// keyFrameSizes returns each key frame the recorder read, by its first
// packet, with the size its VP8 frame header gives (RFC 6386 9.1).
func keyFrameSizes(tr *corpus.Trace) []keyFrameRead {
	var out []keyFrameRead
	seen := map[uint32]bool{}

	for _, r := range tr.Records {
		if r.T != corpus.RecordRTP || r.Pad || seen[r.TS] {
			continue
		}

		var vp8 codecs.VP8Packet
		if _, err := vp8.Unmarshal(r.Pay); err != nil || vp8.S == 0 || len(vp8.Payload) < 10 || vp8.Payload[0]&1 != 0 {
			continue
		}

		seen[r.TS] = true
		out = append(out, keyFrameRead{
			size: fmt.Sprintf("%dx%d",
				binary.LittleEndian.Uint16(vp8.Payload[6:])&0x3FFF, binary.LittleEndian.Uint16(vp8.Payload[8:])&0x3FFF),
			at: time.UnixMicro(tr.Header.EpochUnixUs + r.AtUs),
		})
	}

	return out
}
