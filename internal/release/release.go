// Package release computes a release of the recorder from its history: the
// next version, the CHANGELOG.md section and the GitHub release notes.
// cmd/release runs it; docs/releasing.md describes the process.
package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a release version, as VERSION and the tags carry it.
type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Tag is the version's git tag.
func (v Version) Tag() string {
	return "v" + v.String()
}

// Bump returns the version after a patch, minor or major release.
func (v Version) Bump(kind string) (Version, error) {
	switch kind {
	case "patch":
		return Version{v.Major, v.Minor, v.Patch + 1}, nil
	case "minor":
		return Version{v.Major, v.Minor + 1, 0}, nil
	case "major":
		return Version{v.Major + 1, 0, 0}, nil
	}

	return Version{}, fmt.Errorf("bump %q: want patch, minor or major", kind)
}

// ParseVersion parses "X.Y.Z", with or without a leading "v".
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q: want X.Y.Z", s)
	}

	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, fmt.Errorf("version %q: want X.Y.Z", s)
		}
		n[i] = v
	}

	return Version{n[0], n[1], n[2]}, nil
}

// ParseVersionFile parses VERSION: the version and the package release
// number, which the deb package takes as its release.
func ParseVersionFile(s string) (Version, int, error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return Version{}, 0, fmt.Errorf("VERSION %q: want \"X.Y.Z N\"", strings.TrimSpace(s))
	}

	v, err := ParseVersion(fields[0])
	if err != nil {
		return Version{}, 0, err
	}

	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 1 {
		return Version{}, 0, fmt.Errorf("VERSION %q: the package release must be a positive number", strings.TrimSpace(s))
	}

	return v, n, nil
}

// VersionFile is VERSION's content for v, whose package release starts at 1.
func VersionFile(v Version) string {
	return v.String() + " 1\n"
}

var (
	// The commit types the published sections list
	// (TestEntriesReproduceReleasedSections), and git's own revert subject.
	// test, docs, style and chore commits, and subjects with no type, are
	// left out.
	listed = regexp.MustCompile(`^((feat|fix|build|ci|refactor|perf|revert)(\([^)]*\))?!?: |Revert ")`)
	// An earlier release's own commit, squash-merged or not.
	releaseCommit = regexp.MustCompile(`^build: v\d+\.\d+\.\d+( \(#\d+\))?$`)
)

// Entries are the subjects a release lists, in the order given.
func Entries(subjects []string) []string {
	var entries []string
	for _, s := range subjects {
		if listed.MatchString(s) && !releaseCommit.MatchString(s) {
			entries = append(entries, s)
		}
	}

	return entries
}

// Section is v's CHANGELOG.md section.
func Section(v Version, entries []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", v.Tag())
	for _, e := range entries {
		fmt.Fprintf(&b, "* %s\n", e)
	}

	return b.String()
}

const unreleased = "### UNRELEASED"

// InsertSection puts section above the newest release in changelog.
func InsertSection(changelog, section string) (string, error) {
	if strings.Contains(changelog, "\r") {
		return "", fmt.Errorf("CHANGELOG.md has CRLF line endings: convert it to LF first")
	}

	// A leading newline lets a heading on the first line match "\n### " too.
	c := "\n" + changelog
	if strings.Contains(c, "\n"+unreleased+"\n") {
		return "", fmt.Errorf("CHANGELOG.md has an %s section: fold it into the releases by hand first", unreleased)
	}

	heading, _, _ := strings.Cut(section, "\n")
	if strings.Contains(c, "\n"+heading+"\n") {
		return "", fmt.Errorf("CHANGELOG.md already has %s", heading)
	}

	i := strings.Index(c, "\n### ")
	if i < 0 {
		return strings.TrimRight(changelog, "\n") + "\n\n" + section, nil
	}

	return c[1:i+1] + section + "\n" + c[i+1:], nil
}

// Notes are v's GitHub release notes: its CHANGELOG.md entries, and a link
// to the changes since the release below it. repoURL is the repository's
// web address.
func Notes(changelog string, v Version, repoURL string) (string, error) {
	if strings.Contains(changelog, "\r") {
		return "", fmt.Errorf("CHANGELOG.md has CRLF line endings: convert it to LF first")
	}

	sections := strings.Split("\n"+changelog, "\n### ")
	for i, s := range sections {
		heading, body, _ := strings.Cut(s, "\n")
		if heading != v.Tag() {
			continue
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## What's Changed\n\n%s\n", strings.TrimSpace(body))
		if i+1 < len(sections) {
			prev, _, _ := strings.Cut(sections[i+1], "\n")
			fmt.Fprintf(&b, "\n**Full Changelog**: %s/compare/%s...%s\n", strings.TrimRight(repoURL, "/"), prev, v.Tag())
		}

		return b.String(), nil
	}

	return "", fmt.Errorf("CHANGELOG.md has no %s section", v.Tag())
}
