package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeJSON(t *testing.T, v any) map[string]any {
	t.Helper()

	b, err := json.Marshal(v)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	return m
}

func TestRecordingMediaStartedCarriesBothClocks(t *testing.T) {
	start := time.Now().Add(-time.Second)

	m := decodeJSON(t, NewRecordingMediaStarted("s1", start))

	assert.Equal(t, map[string]any{
		"id":                 "recordingMediaStarted",
		"recordingSessionId": "s1",
		"mediaStartUTC":      float64(start.UnixMilli()),
		"mediaStartHR":       float64(MonotonicMs(start)),
	}, m)
}

func TestRecordingStoppedCarriesTheMediaSpan(t *testing.T) {
	start := time.Now().Add(-10 * time.Second)
	end := start.Add(9500 * time.Millisecond)

	stopped := NewRecordingStopped("s1", StopReasonNormal, 9500).WithMediaSpan(types.MediaSpan{Start: start, End: end})
	m := decodeJSON(t, stopped)

	assert.Equal(t, float64(start.UnixMilli()), m["mediaStartUTC"])
	assert.Equal(t, float64(MonotonicMs(start)), m["mediaStartHR"])
	assert.Equal(t, float64(end.UnixMilli()), m["mediaEndUTC"])
	assert.Equal(t, float64(MonotonicMs(end)), m["mediaEndHR"])
	// timestampUTC and timestampHR keep their form for older controllers.
	assert.IsType(t, "", m["timestampUTC"])
	assert.Equal(t, float64(9500), m["timestampHR"])
}

func TestRecordingStoppedWithoutMediaOmitsTheSpan(t *testing.T) {
	m := decodeJSON(t, NewRecordingStopped("s1", StopReasonNormal, 0))

	for _, k := range []string{"mediaStartUTC", "mediaStartHR", "mediaEndUTC", "mediaEndHR"} {
		assert.NotContains(t, m, k)
	}
}

func TestRecorderStatusListsMediaTimestamps(t *testing.T) {
	m := decodeJSON(t, NewRecorderStatus("v1", "i1"))

	assert.Equal(t, []any{"mediaTimestamps"}, m["capabilities"])
}

func TestRecordingInfoCarriesTheMediaStart(t *testing.T) {
	start := time.Now()
	info := &RecordingInfo{SessionId: "s1"}
	info.SetMediaStart(start)

	m := decodeJSON(t, info)
	assert.Equal(t, float64(start.UnixMilli()), m["mediaStartUTC"])
	assert.Equal(t, float64(MonotonicMs(start)), m["mediaStartHR"])
}
