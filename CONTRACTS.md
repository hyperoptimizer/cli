# Public contracts

`schema.graphql` is a snapshot for the public operations in
`internal/graphql/operations.graphql`. It includes selected output fields and the
public input definitions they require. It includes no server resolvers or internal
service authentication policies. The hosted API remains authoritative for access.

Regenerate with `go generate ./internal/graphql`; CI rejects generated drift.

`internal/config/schema.json`, `template.toml`, `format-policy.json`, `fixtures.json`
and `format-fixtures.json` are committed portable artifacts from Hyperoptimizer's
product contracts. Their original generators remain with the backend. Backend
contract changes must update these artifacts in an explicit reviewed CLI PR; test
fixtures and CLI validation must pass before release. This repository builds and
tests without accessing that generator or the private repository.

Initial extraction: private services commit d326aa6e7d5916846a8af6819ff9cb860ccdbd5e,
30 October 2026. This public repository starts with a fresh history.
