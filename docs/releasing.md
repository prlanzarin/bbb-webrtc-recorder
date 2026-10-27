# Releasing

A release sets `VERSION` and adds a `CHANGELOG.md` section, in a
`build: vX.Y.Z` pull request into `develop`. The commit that merges it is
tagged `vX.Y.Z`, the Docker images are built from the tag, a GitHub release
carries the same list, and `main` moves to the commit. Two workflows do all of
it.

## Cut a release

1. In Actions, run **Prepare a release** with the bump: patch for fixes only,
   minor when a `feat` commit is in, major for an incompatible change.
2. It opens a `build: vX.Y.Z` pull request into `develop`, with `VERSION` and a
   `CHANGELOG.md` section. The section lists, oldest first, the `feat`,
   `fix`, `build`, `ci`, `refactor`, `perf` and `revert` commits since the
   last tag, and the revert commits git and GitHub write (`Revert "..."`). It
   leaves out merges and `chore`, `test`, `docs`, `style` and untyped
   subjects. To reword an entry, edit `CHANGELOG.md` on the branch.
3. Merge it. **Publish a release** then tags the merge and starts **Publish
   Docker image** on the tag. It creates the GitHub release from the merged
   section and a compare link, and moves `main` to the tagged merge.

A pull request merged into `develop` while the release one is open ships in
that release without a `CHANGELOG.md` entry. Add the entry on the release
branch, or close the release pull request and run Prepare a release again.

## Preview or release by hand

From the repository root:
- `go run ./cmd/release prepare -bump minor -dry-run` prints the next section,
  with the same checks as a real run, and writes nothing.
- `go run ./cmd/release prepare -bump minor` writes `VERSION` and the section.
  Pushing both to `develop` in one commit starts Publish a release, as a merge
  does.
- `go run ./cmd/release notes` prints the GitHub release notes of the version
  in `VERSION`.

## When something fails

- **Prepare a release** stops:
  - when the last tag is not the version in `VERSION`, as after a publish that
    never tagged;
  - when the next tag exists;
  - when no commit since the last tag is listed. To release anyway, write
    `VERSION` and the section by hand and push them in one commit;
  - when `CHANGELOG.md` has an `UNRELEASED` section. Fold that into the
    releases by hand first;
  - when a pull request from `release/vX.Y.Z` is still open.
- **Publish a release** checks `VERSION`, the `CHANGELOG.md` section and an
  existing tag before it changes anything. Re-run it to finish a failed
  publish:
  - an existing tag is kept;
  - the Docker images are built again, unless the GitHub release exists;
  - once the GitHub release exists, only `main` is left to move.

  It also runs by hand, on `develop` only. If the tag is missing, it tags
  `develop`'s tip, so run it before anything else merges.
- **Moving `main`** fails when `main` has commits that `develop` lacks. Merge
  `main` into `develop`; the next release then moves `main` past both. To move
  it now, push that merge to `main` by hand.
- **A Publish run cancelled while waiting** was replaced by a later run that
  GitHub queued after it. Re-run it from its page, which keeps its commit.
- **Only the package release number changed** in `VERSION` (`0.18.0 2`):
  nothing is published, since that version's release exists.

## Setup

- **Pull requests:** the repository must let GitHub Actions create pull
  requests, in Settings, Actions, General, under Workflow permissions.
- **Pushes:** Prepare a release pushes `release/vX.Y.Z`. Publish a release
  pushes the tag and `main`. Both use the workflow's token, or Publish uses
  `RELEASE_TOKEN` when it is set. A protection rule or ruleset on those refs
  must let GitHub Actions, or that token's owner, through.
- **`RELEASE_TOKEN`:** GitHub can refuse a push made with the workflow's token
  when the commits change workflow files. If the tag or `main` push is
  refused, add a `RELEASE_TOKEN` secret: a fine-grained personal access token
  with Contents and Workflows read and write access to this repository.
  Publish a release then pushes with it, and the tag push starts Publish
  Docker image by itself.
- **CI on the release pull request:** none runs, because the workflow's token
  opens it, and that starts no workflow. A push to the branch by a person, such
  as a reworded entry, starts it. If `develop` ever requires status checks, the
  release pull request needs such a push, or an admin's override, to merge.
