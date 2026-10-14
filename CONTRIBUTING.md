# Contributing

Build and test steps are in [docs/testing.md](docs/testing.md). Run `make check-all` before sending a change.

A new tool or client method needs a live test. The suites fail without one.

## Releases

- Changes collect under "Unreleased" in [CHANGELOG.md](CHANGELOG.md).
- A version is tagged when a batch is worth upgrading for, not on a schedule.
- Breaking changes are grouped into one release, not shipped one per release.
