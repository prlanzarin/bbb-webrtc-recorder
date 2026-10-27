// Command release prepares and describes a recorder release; see
// docs/releasing.md. Run it from the repository root.
//
//	release prepare -bump patch|minor|major [-dry-run]
//	release notes [-version X.Y.Z] [-repo URL]
//	release version
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bigbluebutton/bbb-webrtc-recorder/internal/release"
)

const (
	versionFile   = "VERSION"
	changelogFile = "CHANGELOG.md"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	var err error
	switch os.Args[1] {
	case "prepare":
		err = prepare(os.Args[2:])
	case "notes":
		err = notes(os.Args[2:])
	case "version":
		var v release.Version
		if v, err = readVersion(); err == nil {
			fmt.Println(v)
		}
	default:
		usage()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: release prepare -bump patch|minor|major [-dry-run]\n"+
		"       release notes [-version X.Y.Z] [-repo URL]\n"+
		"       release version")
	os.Exit(2)
}

// prepare writes the next release's VERSION and CHANGELOG.md section, from
// the commits since the last tag, and prints the section.
func prepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	bump := fs.String("bump", "", "patch, minor or major")
	dryRun := fs.Bool("dry-run", false, "print the section without writing VERSION or CHANGELOG.md")
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return fmt.Errorf("prepare takes no arguments, got %q", fs.Args())
	}

	current, err := readVersion()
	if err != nil {
		return err
	}

	// The commits since the last tag belong to the next release only if that
	// tag matches VERSION. A mismatch means the last release was never
	// tagged, as when its publish failed.
	last, err := git("describe", "--tags", "--abbrev=0", "--match", "v*")
	if err != nil {
		return err
	}
	if last != current.Tag() {
		return fmt.Errorf("the last tag is %s, but VERSION says %s", last, current)
	}

	next, err := current.Bump(*bump)
	if err != nil {
		return err
	}
	if _, err := git("rev-parse", "--quiet", "--verify", "refs/tags/"+next.Tag()); err == nil {
		return fmt.Errorf("%s is already tagged", next.Tag())
	}

	log, err := git("log", "--reverse", "--no-merges", "--format=%s", last+"..HEAD")
	if err != nil {
		return err
	}
	entries := release.Entries(strings.Split(log, "\n"))
	if len(entries) == 0 {
		return fmt.Errorf("nothing to release: no feat, fix, build, ci, refactor, perf or revert commit since %s", last)
	}

	section := release.Section(next, entries)

	changelog, err := os.ReadFile(changelogFile)
	if err != nil {
		return err
	}

	updated, err := release.InsertSection(string(changelog), section)
	if err != nil {
		return err
	}

	if !*dryRun {
		if err := os.WriteFile(changelogFile, []byte(updated), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(versionFile, []byte(release.VersionFile(next)), 0o644); err != nil {
			return err
		}
	}

	fmt.Print(section)

	return nil
}

// notes prints a release's GitHub release notes from CHANGELOG.md.
func notes(args []string) error {
	repo := "https://github.com/bigbluebutton/bbb-webrtc-recorder"
	if server, name := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"); server != "" && name != "" {
		repo = server + "/" + name
	}

	fs := flag.NewFlagSet("notes", flag.ExitOnError)
	version := fs.String("version", "", "the release, X.Y.Z (default: VERSION's)")
	fs.StringVar(&repo, "repo", repo, "the repository's web address, for the compare link")
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return fmt.Errorf("notes takes no arguments, got %q: use -version", fs.Args())
	}

	v, err := readVersion()
	if *version != "" {
		v, err = release.ParseVersion(*version)
	}
	if err != nil {
		return err
	}

	changelog, err := os.ReadFile(changelogFile)
	if err != nil {
		return err
	}

	text, err := release.Notes(string(changelog), v, repo)
	if err != nil {
		return err
	}

	fmt.Print(text)

	return nil
}

func readVersion() (release.Version, error) {
	raw, err := os.ReadFile(versionFile)
	if err != nil {
		return release.Version{}, err
	}

	v, _, err := release.ParseVersionFile(string(raw))

	return v, err
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
	}

	return strings.TrimSpace(string(out)), err
}
