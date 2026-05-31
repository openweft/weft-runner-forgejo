# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Extend `TaskSummary` to mirror the Forgejo `runner.v1.Task` proto.

## [v0.1.0] — 2026-05-30

### Added

- Initial skeleton: Go module, CLI, runner package boundaries.
- Real Connect-over-JSON integration — `Register` works, `Run` long-polls tasks.
- In-VM Forgejo runner image plus Connect-over-JSON shim tests.
- CI: build + test on push/PR across linux amd64+arm64 matrix.
