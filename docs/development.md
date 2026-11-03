English | [简体中文](development.zh-CN.md)

# Development

## Checks

```sh
go run mvdan.cc/gofumpt@v0.12.0 -w .
go vet ./...
go test ./...
make build
golangci-lint run
actionlint
```

CI uses golangci-lint v2.15.0 and actionlint v1.7.12. `make release` runs nfpm v2.48.0, and CI installs and removes the `.deb` it builds.

| Target | Action |
| --- | --- |
| `make build` | Build `dist/verideploy` |
| `make check` | Run tests, build and lint |
| `make release VERSION=<tag>` | Build `verideploy-linux-amd64`, its `.deb` and `.rpm` (nfpm, configured in `nfpm.yaml`) and `SHA256SUMS` into `dist/release` |

Pushing a `v*` tag starts the release workflow, which:

1. runs `make release`;
2. attests every asset in `dist/release` with GitHub artifact attestation;
3. publishes a GitHub Release.

## Tests

The tests need no SEV-SNP hardware. A fake host tree, command runner, VeriProv and CVM stand in.

| Fixture | Content |
| --- | --- |
| `internal/release/testdata/rel-2026.10.30-1/` | `chain.json`, `build-records.json` and `SHA256SUMS` of a published release. Its Build Records must verify under the official Release Key. |
| `internal/plan/testdata/od3-verirelay.service` | A production unit launched by hand. The derived QEMU command line must equal it, apart from file paths. |
| `internal/plan/testdata/verirelay-relay.service` | The unit VeriDeploy renders for the same instance. |

To update the rendered unit after review:

```sh
go test ./internal/plan -run TestProductionArgv -update
```
