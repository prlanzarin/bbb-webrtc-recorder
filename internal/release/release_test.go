package release

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	v, n, err := ParseVersionFile("0.18.3 2\n")
	require.NoError(t, err)
	assert.Equal(t, Version{0, 18, 3}, v)
	assert.Equal(t, 2, n)
	assert.Equal(t, "v0.18.3", v.Tag())

	for kind, want := range map[string]string{"patch": "0.18.4", "minor": "0.19.0", "major": "1.0.0"} {
		next, err := v.Bump(kind)
		require.NoError(t, err)
		assert.Equal(t, want, next.String(), kind)
	}

	_, err = v.Bump("auto")
	assert.Error(t, err)
	assert.Equal(t, "0.19.0 1\n", VersionFile(Version{0, 19, 0}))

	for _, bad := range []string{"0.18.0", "0.18 1", "0.18.0 0", "0.18.x 1", "0.18.0 1 2"} {
		_, _, err := ParseVersionFile(bad)
		assert.Error(t, err, bad)
	}
}

func TestEntries(t *testing.T) {
	assert.Equal(t, []string{
		"feat!: drop the old option",
		"fix(livekit)!: change a default",
		`Revert "fix(livekit): X"`,
		"revert: fix: Y",
		"perf(webm): write less",
	}, Entries([]string{
		"feat!: drop the old option",
		"fix(livekit)!: change a default",
		`Revert "fix(livekit): X"`,
		"revert: fix: Y",
		"perf(webm): write less",
		"chore: update go.mod",
		"test(e2e): cover X",
		"docs: say Y",
		"style: format",
		"fixup! fix: Z",
		"untyped subject",
		"build: v0.18.0",
		"build: v0.19.0 (#52)",
	}))
}

// The rule must give the sections the last releases were published with.
func TestEntriesReproduceReleasedSections(t *testing.T) {
	changelog := readChangelog(t)

	for _, tag := range []string{"v0.17.0", "v0.18.0"} {
		raw, err := os.ReadFile("testdata/" + tag + ".subjects")
		require.NoError(t, err)

		v, err := ParseVersion(tag)
		require.NoError(t, err)

		section := Section(v, Entries(strings.Split(strings.TrimSpace(string(raw)), "\n")))
		assert.Contains(t, changelog, "\n"+section+"\n", tag)
	}
}

func TestInsertSection(t *testing.T) {
	const intro = "# Changelog\n\nAll notable changes to this project will be documented in this file.\n"
	section := Section(Version{0, 19, 0}, []string{"feat: a", "fix: b"})

	got, err := InsertSection(intro+"\n### v0.18.0\n\n* fix: c\n", section)
	require.NoError(t, err)
	assert.Equal(t, intro+"\n### v0.19.0\n\n* feat: a\n* fix: b\n\n### v0.18.0\n\n* fix: c\n", got)

	got, err = InsertSection(intro, section)
	require.NoError(t, err)
	assert.Equal(t, intro+"\n"+section, got, "a first release")

	got, err = InsertSection("### v0.18.0\n\n* fix: c\n", section)
	require.NoError(t, err)
	assert.Equal(t, section+"\n### v0.18.0\n\n* fix: c\n", got, "a heading on the first line")

	_, err = InsertSection(intro+"\n### v0.19.0\n\n* fix: c\n", section)
	assert.ErrorContains(t, err, "already has ### v0.19.0")

	_, err = InsertSection("### v0.19.0\n\n* fix: c\n", section)
	assert.ErrorContains(t, err, "already has ### v0.19.0", "on the first line")

	_, err = InsertSection(intro+"\n### UNRELEASED\n\n* fix: c\n", section)
	assert.ErrorContains(t, err, "UNRELEASED")

	_, err = InsertSection(strings.ReplaceAll(intro+"\n### v0.18.0\n\n* fix: c\n", "\n", "\r\n"), section)
	assert.ErrorContains(t, err, "CRLF")
}

func TestNotes(t *testing.T) {
	changelog := readChangelog(t)

	notes, err := Notes(changelog, Version{0, 18, 0}, "https://github.com/bigbluebutton/bbb-webrtc-recorder/")
	require.NoError(t, err)
	assert.Equal(t, "## What's Changed\n\n"+
		"* fix(app): shut down gracefully on SIGTERM too\n"+
		"* feat(corpus): model senders whose media clock runs slow\n"+
		"* fix(webm): skip an Opus frame whose timestamp goes back\n"+
		"* fix(livekit): keep microphone recordings on the wall clock\n"+
		"* feat(appstats): export the microphones' media clock\n\n"+
		"**Full Changelog**: https://github.com/bigbluebutton/bbb-webrtc-recorder/compare/v0.17.0...v0.18.0\n", notes)

	notes, err = Notes("### v0.1.0\n\n* feat: first\n", Version{0, 1, 0}, "https://example.com")
	require.NoError(t, err)
	assert.Equal(t, "## What's Changed\n\n* feat: first\n", notes, "the oldest release has no compare link")

	_, err = Notes(changelog, Version{9, 9, 9}, "https://example.com")
	assert.ErrorContains(t, err, "no v9.9.9 section")
}

func readChangelog(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile("../../CHANGELOG.md")
	require.NoError(t, err)

	return string(raw)
}
