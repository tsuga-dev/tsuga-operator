# Changelog

All notable changes to the Tsuga Operator are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `Monitor` `spec.folderId` files a monitor in an existing Tsuga monitor folder.
  When unset, the monitor keeps its current folder.

## [0.1.0] - 2026-10-07

### Added

- First public release: `Dashboard`, `Monitor`, `SLO`, `TsugaCollectorConfig`,
  `TsugaPostgresMonitoring` and `TsugaMonitoring` resources in `observability.tsuga.com/v1alpha1`.
