# Recorder end-to-end tests

These tests run the recorder binary against a real `livekit-server` and
`redis-server` on loopback. They drive the recorder over Redis the way
bbb-webrtc-sfu does, and publish generated media from a Go LiveKit
participant (lksdk) running inside the test process. Nothing runs in Docker.
Every process gets its own free loopback ports, so the suite never touches
LiveKit, Redis or recorder instances already running on the host. A child
that loses a port to another process before binding it is started once more
on fresh ones. LiveKit gathers candidates on `lo` only, and hands clients a
STUN server on a loopback port nothing listens on, so no traffic leaves the
host.

The files carry the `e2e` build tag, so a plain `go test ./...` skips them.

## What runs

For each test, the harness:
- starts `livekit-server` from a generated loopback-only config using the
  well-known dev key pair (`devkey`/`secret`), and a `redis-server`;
- starts the recorder, built once per run from `./cmd/bbb-webrtc-recorder`.
  Its generated config turns on Prometheus, trace capture (header mode) and
  the stats file, and gives it its own recordings directory;
- publishes VP8 and Opus tracks from `internal/corpus` generator schedules
  in real time. The publisher answers each PLI with a key frame after a set
  delay, paced over a set spread, and can withhold packets (loss nothing
  repairs) or pause whole frames.

Each test checks the WebM through `internal/corpus/oracle`, along with the
recorder's trace, the `recordingStopped` events and the `/metrics` deltas.

| Test | Scenario |
|---|---|
| `TestS1Baseline` | **S1, baseline.** A camera and a microphone, one session per track, about 10 s on a clean path. It checks the frame counts against what was sent and the PTS (increasing, no duplicates, gaps bounded), that the trace and the metrics agree, and that the recorder asks for no key frames. |
| `TestS2UnrepairedLoss` | **S2, unrepaired loss.** One packet is withheld from the middle of a camera frame, at 15 and at 5 fps. The trace must show exactly one unrepaired gap, at that packet. The recorder must request a key frame early, from the sequence gap (trace PLI reason `packet_drop`), within a bound that depends on the fps, and frames must be written again after the publisher answers. |
| `TestS3PacedKeyframes` | **S3, paced key frames.** A 3.4 fps camera whose key frames are paced over 664 ms, plus one lost packet. Key-frame requests must stay bounded (no keyframe loop) and frames must keep being written. |
| `TestPublisherSendLoop`, `TestVP8PayloadHelpers` | The publisher's send loop (PLI answers, pacing, drops, pauses) and the VP8 payload helpers, with no servers involved. |

## Running locally

You need Go (the version in `go.mod`), `livekit-server` 1.13.7 and
`redis-server`.

```sh
LIVEKIT_SERVER_BIN=/usr/bin/livekit-server \
  go test -tags e2e -v -timeout 15m ./tests/e2e/...

# One scenario, three times
go test -tags e2e -run 'TestS1' -count=3 -v ./tests/e2e/...
```

Environment:

| Variable | Effect |
|---|---|
| `LIVEKIT_SERVER_BIN` | The `livekit-server` binary. Default: `livekit-server` on `PATH`. |
| `REDIS_SERVER_BIN` | The `redis-server` binary. Default: `redis-server` on `PATH`. |
| `REDIS_ADDR` | Use this Redis instead of starting one. Channel names get a suffix naming the run (stamp and PID) and the test, so concurrent runs and tests do not cross. |
| `E2E_RECORDER_BIN` | Use this recorder binary instead of building one. |
| `E2E_RECORDER_RACE=1` | Build the recorder with `-race`; a test fails when its recorder's log reports a data race. The race build needs a lot of cache space. |
| `E2E_ARTIFACTS` | Root directory for artifacts. When set, every test's artifacts are kept. |
| `E2E_KEEP_ARTIFACTS=1` | Keep the artifacts of passing tests under the default root. |
| `E2E_RECORDER_LOG_LEVEL`, `E2E_LIVEKIT_LOG_LEVEL` | Log levels. Defaults: `debug` and `info`. |

The child processes do not inherit `BBBRECORDER_*`, `LIVEKIT_*`, `REDIS_*`,
`NODE_IP` or `UDP_PORT`, so a shell set up for a dev recorder cannot override
the generated configs. Each child runs in its own process group and is
killed when its test ends, and also when the test binary dies on a panic or
a `-timeout`.

### Artifacts

Each test writes to `<run>/<Test>-<n>/`. With `E2E_ARTIFACTS` set, `<run>`
is `$E2E_ARTIFACTS/<stamp>-<pid>`. Otherwise it is a new private directory,
`$TMPDIR/bwr-e2e-*`, and a passing test's directory is removed. A directory
holds:

```
bbb-webrtc-recorder.{yml,log}  livekit-server.{yaml,log}  redis-server.log  lksdk-test-process.log
pubsub.ndjson                                    # every Redis message sent and received
publisher.<identity>.<trackSID>.{sent.ndjson.gz,summary.json}
recorder/recordings/<room>/<trackSID>-<session>.webm
                          /<trackSID>-<session>.<trackSID>.trace.ndjson.gz
                          /<trackSID>-<session>-stats.json
                          /<trackSID>-<session>.summary.json
```

A test run takes a few hundred KB.
