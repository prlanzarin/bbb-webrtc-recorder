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
| `TestS4NACKUnderLoss` | **S4, NACK/RTX.** `livekit-server` and `redis-server` run in one network namespace and the recorder in another, and netem drops 3% of what the servers send the recorder. The test checks that the recorder's ICE pair uses the link between the namespaces, that lost packets were read later (by sequence number, whichever SSRC carried them), that nearly every frame was written, and that there was no keyframe loop. It needs root and `E2E_NETEM=1`, and is skipped otherwise, or fails with `E2E_NETEM_REQUIRED=1`. |
| `TestS4NetemControl`, `TestS4NetemPlan` | S4's stack and checks without the namespaces or the loss, with loopback addresses standing in for the links, and the namespace plan and config edits. Both run unprivileged. The control cannot show that ICE uses the link between the namespaces; only S4 can. |
| `TestS5SignalCutResumes` | **S5, signal cut.** A TCP proxy resets the recorder's signalling connections and lets the redial through. Each session resumes in place, in its one file. |
| `TestS5SignalRefusedSplits` | **S5, signal refused.** The proxy also refuses redials for 5 s. The SDK falls back to a full reconnect and every session stops as `closed`. The test retries as the SFU does, so each session ends with two files. This pins today's behaviour; it is not a target. |
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
| `REDIS_ADDR` | Use this Redis instead of starting one. Channel names get a suffix naming the run (stamp and PID) and the test, so concurrent runs and tests do not cross. S4 and its control always start their own. |
| `E2E_RECORDER_BIN` | Use this recorder binary instead of building one. |
| `E2E_RECORDER_RACE=1` | Build the recorder with `-race`; a test fails when its recorder's log reports a data race. The race build needs a lot of cache space. |
| `E2E_ARTIFACTS` | Root directory for artifacts. When set, every test's artifacts are kept. |
| `E2E_KEEP_ARTIFACTS=1` | Keep the artifacts of passing tests under the default root. |
| `E2E_RECORDER_LOG_LEVEL`, `E2E_LIVEKIT_LOG_LEVEL` | Log levels. Defaults: `debug` and `info`. |
| `E2E_NETEM=1` | Allow S4's privileged path. The test must also run as root. |
| `E2E_NETEM_REQUIRED=1` | Fail S4 instead of skipping it when the privileged path cannot run. |

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

### S4 (root)

S4 builds two network namespaces and three veth links, with addresses from
three /30s in `198.18.0.0/15` derived from the test's PID:
- the server namespace runs `livekit-server` and `redis-server`, which
  listen on both of its addresses;
- the recorder namespace runs the recorder;
- host <-> server: the test's publisher and Redis driver reach the servers
  here;
- host <-> recorder: only the test's scrape of the recorder's metrics;
- server <-> recorder: the recorder's signalling, Redis and media. netem
  drops 3% of the server end's egress, so only what the servers send the
  recorder is lost.

Every connection that enters the host namespace is opened from the host,
and the replies to it are established traffic; the publisher's own ICE
checks set that up for LiveKit's media. S4 therefore does not depend on the
host firewall, which may drop new inbound connections. It turns on no
forwarding, adds no route and changes no firewall rule. The publisher's
checks toward the servers' recorder-side address follow the host's default
route and are lost there, as that range is never routed.

The kernel needs the `sch_netem` module. Build both binaries as your user
into a private directory, then run only the test binary as root, with its
artifacts in that directory too:

```sh
d=$(mktemp -d) &&
  go build -o "$d/bwr" ./cmd/bbb-webrtc-recorder &&
  go test -c -tags e2e -o "$d/e2e.test" ./tests/e2e &&
  cd tests/e2e && sudo env "PATH=$PATH" "LIVEKIT_SERVER_BIN=$LIVEKIT_SERVER_BIN" \
    "REDIS_SERVER_BIN=$REDIS_SERVER_BIN" E2E_NETEM=1 E2E_RECORDER_BIN="$d/bwr" \
    E2E_ARTIFACTS="$d/artifacts" "$d/e2e.test" -test.run '^TestS4NACKUnderLoss$' -test.v
```

`go test -tags e2e -v -run TestS4NetemPlan ./tests/e2e/...` logs the exact
setup and teardown commands for a sample PID.

Do this only on a disposable machine: it changes the host's network
configuration while it runs. The test removes the namespaces and their
links when it ends, and at startup it removes any that a dead run left
behind.

S4's bounds are derived from the loss rate, packets per frame and the
recorder's keyframe requests (see `netem_test.go`). Netem dropping a packet
and every retransmission of it is rare. Most unrepaired losses come from
LiveKit: after a subscriber's PLI, it stops retransmitting on that downtrack
until it forwards a key frame, and it does not forward a PLI within 500 ms
of the previous one. So a dropped request leaves one outage, until the
recorder's next request up to 1 s later. Frames are compared from the
first one written: when netem drops the first packet of the first key
frame, no NACK can name it, and the recording starts only once the jitter
buffer's 2 s have passed.

## CI

`.github/workflows/e2e.yml` runs on `ubuntu-latest`:
- nightly;
- on demand (`workflow_dispatch`);
- on pull requests that touch the recorder (`internal/`, `cmd/`,
  `third_party/`, `tests/e2e/`, `go.mod`, `go.sum` or the workflow itself),
  when opened, pushed to or reopened;
- on any other pull request that carries the `e2e` label: when the label is
  added, on every later push, and when the PR is reopened.

A `changes` job lists the paths a PR changes, against the base it would
merge into. A newer run for a PR cancels the one in progress; a run skipped
for another label does not.

A separate `vet` job runs `go vet -tags e2e ./tests/e2e/...` on every pull
request, labelled or not, and on every other trigger, since the per-push
workflows build without the tag.

The e2e job sets up Go from `go.mod` and downloads `livekit-server` 1.13.7 from
the official GitHub release, checking it against a pinned sha256. The tarball
is cached under that version and checksum, and checked again on a cache hit.
It installs `redis-server` from apt and stops the system service, then runs
two steps:
1. `go test -tags e2e -v -timeout 15m ./tests/e2e/...`, the whole suite
   unprivileged. S4's privileged test skips here.
2. S4 as root, as shown above, with `-test.run '^TestS4NACKUnderLoss$'` and
   `E2E_NETEM_REQUIRED=1`, so a skip fails the step. This step runs even if
   step 1 failed, but not if setup did, and has its own 10-minute limit. It
   loads `sch_netem`, and installs `linux-modules-extra` for the running
   kernel if the module is missing.

`E2E_ARTIFACTS` points at `$RUNNER_TEMP/e2e-artifacts`, so every test's
artifacts are kept. When the job fails or is cancelled (as a timeout does),
it uploads them (14-day retention), after a `chown`, because S4 writes them
as root with mode 0600.

To bump LiveKit, change `LIVEKIT_VERSION` and `LIVEKIT_SHA256` together. The
checksum is the `linux_amd64` line of the release's `checksums.txt`.

## Time budget

One local run of the whole suite (recorder build cached) took 105 s
for the package:

| Test | Time |
|---|---|
| S1 | 12 s |
| S2 (both rates) | 21 s |
| S3 | 22 s |
| S4 control | 10 s |
| S5 cut, S5 refused | 14 s, 19 s |
| The rest | under 1 s |

S4 under netem records for 20 s, so expect about 30 s for it. In CI, the
module download and the two builds add a few minutes on a cold cache. The
limits leave plenty of headroom: 15 min for the suite, 5 min for S4's test
binary (10 min for its step), and 30 min for the job.
