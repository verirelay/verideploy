# Security policy

## Reporting a vulnerability

Report vulnerabilities privately to the maintainers. If the repository Security tab offers **Report a vulnerability**, use that private reporting channel.

## Scope

Deploy checks:

- release files against `SHA256SUMS` and `chain.json`;
- the Build Record of the chosen Launch Measurement, signed by a trusted Release Key;
- Registry's answer for that measurement, judged as the Verifier's code verdict does;
- the Bootstrap fetched from VeriProv: its bytes against the `HOST_DATA` served with them, and the VeriProv and profile it names;
- that only root can change the files root reads and boots;
- the host before launch;
- a fresh attestation of the running CVM against what it launched.

Any of these is in scope:

- launching boot files or parameters other than those the release and config describe;
- accepting a Build Record not signed by a trusted Release Key, or naming another release;
- launching a release whose code verdict the Verifier fails;
- launching with a Bootstrap that names another VeriProv or profile than the config;
- a user other than root changing what QEMU, run as root, boots or attaches;
- a unit or QEMU option injected through the config file, the Bootstrap or release metadata;
- an unexpected network destination.

The official Release Key is pinned in the binary, independently of Registry. `release_keys` replaces it with keys you trust by other means.

Downloads follow redirects only to HTTPS, or HTTP on a loopback host. VeriProv and AMD KDS are reached over HTTPS, and Registry over HTTPS or, on a loopback host, HTTP.

Deploy reads no secrets. The Bootstrap, the Config Disk, the unit and `plan.json` are public data.

Issues in VeriRelay, VeriProv or the release pipeline belong to their repositories.

## Limits

- Deploy is outside the trust chain your customers rely on. A compromised Deploy can misconfigure or stop the CVM, or print a Launch Measurement that belongs to no release. Receipts from a CVM without a Build Record fail the Verifier's code verdict. Before allowing a measurement in VeriProv, you can compare it with the release notes.
- `fetch`, `up` and `down` run as root, and QEMU runs as root. The Files check covers the paths Deploy and QEMU use, not the rest of the host.
- `status` reads the Attestation Report without checking its AMD signature. VeriProv checks it before provisioning, and the Verifier checks it for your customers.
- Registry non-revocation is an online observation, not a signed assertion.
- The VeriProv check uses this host's CA roots, which may differ from the CA bundle inside the CVM.
