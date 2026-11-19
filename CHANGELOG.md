# Changelog

All notable changes to the OpenPersona platform. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org).

## [Unreleased]

### Added

- `openpersona-cloud`: the platform's own command. `serve` is the always-on API server (it was `openpersona serve -cloud`) and now serves gRPC over cleartext HTTP/2 behind Caddy; `registry`, `push`, `pull` and `keys` moved here from the library's CLI.
- The Lambda function serves the API's RPCs over the Connect protocol next to HTTP.

### Changed

- Split out of the library into its own module, `github.com/VicoAritonang/open-persona-platform`, built against the library with a `replace` until it moves to its own repository.
- `registry` moved here from `go.vicoworks.com/open-persona/registry`; it implements `server.Authenticator`, `server.JobStore` and `server.UsageRecorder` (the library's former `cloud` package). `registry.NewKey`, `HashKey` and `KeyPrefix` replace `cloud.NewKey`, `cloud.HashKey` and `cloud.KeyPrefix`.
- EC2: the systemd unit runs `openpersona-cloud serve -packs ...`; Caddy proxies to it over h2c so gRPC works.

No database migration: the schema and its migrations are unchanged.
