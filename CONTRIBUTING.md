# Contributing

Before opening a pull request, run the [checks](docs/development.md#checks) that CI runs.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/).

The cross-repo contracts are:

- the release assets `chain.json`, `SHA256SUMS` and `build-records.json` (defined in [Release](https://github.com/verirelay/release/blob/main/docs/chain-format.md));
- Registry Build Records and revocations (defined in [Registry](https://github.com/verirelay/registry/blob/main/docs/api.md));
- the Bootstrap, the Config Disk and the attestation response (defined in [Protocol](https://github.com/verirelay/protocol/blob/main/docs/guest.md));
- VeriProv's Bootstrap listing `GET /v1/bootstraps` (defined in [VeriProv](https://github.com/verirelay/veriprov/blob/main/docs/api.md));
- the QEMU command line of the launch profile `sev-snp-qemu-direct-boot/1` (described in [VeriForge](https://github.com/verirelay/veriforge/blob/main/docs/qemu.md)).

Coordinate contract changes with the affected repositories.

Deploy manages the CVM's lifecycle only: never add management of VeriProv, its policy, Runtime Config or Receipt stores.
