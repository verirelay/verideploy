English | [简体中文](cli.zh-CN.md)

# CLI reference

## Commands

```sh
verideploy init   [-c FILE] [--template]
verideploy fetch  [-c FILE]
verideploy plan   [-c FILE]
verideploy up     [-c FILE] [--restart] [--timeout DUR]
verideploy down   [-c FILE]
verideploy status [-c FILE]
verideploy version
```

`-c` names the instance's config file, `deploy.toml` by default. Its directory is the instance directory.

| Command | Needs root | What it does |
| --- | --- | --- |
| `init` | yes | Writes a new config file, asking for each value. Never overwrites a file. |
| `fetch` | yes | Downloads the release into `releases/<tag>/` and checks it. A release already there that passes the checks is kept as it is. Otherwise the download replaces it only once it passes. Then fetches the Bootstrap from VeriProv into `bootstrap.json`. |
| `plan` | no | Checks the release and the host, then prints what `up` would launch: the whole unit when none is installed, otherwise its difference from the installed one. Changes nothing. |
| `up` | yes | Runs the `plan` checks, writes `config.img` and the systemd unit, restarts the CVM when it does not run the current plan, and waits for `/readyz`. Changes nothing when a check fails. |
| `down` | yes | Stops and disables the CVM. Files are kept. |
| `status` | no | Checks the unit and `/readyz`, requests a fresh attestation, and compares it with what `up` launched. |

`init` shows each default in brackets; Enter keeps it. With `--template`, it writes the defaults without asking, for you to edit:

| Value | Default |
| --- | --- |
| `instance` | The directory's name when it is a valid instance name, otherwise `relay` |
| `release` | The latest release of `verirelay/release`, looked up on GitHub |
| `veriprov` | None: `init` asks, `--template` leaves it empty |
| `profile` | The only Bootstrap VeriProv serves; with several, `init` asks |
| `machine.vcpus`, `machine.memory` | `8`, `16G` |
| `network.forward` | The first free port on `127.0.0.1` from 18081 |

`init` lists the Bootstraps your VeriProv serves and shows the `HOST_DATA` of the chosen one. A VeriProv that cannot be reached is kept with a warning, and `fetch` tries again. When the latest release cannot be looked up, `init` asks for it and `--template` leaves it empty.

`up` options:

- `--restart` restarts the CVM even when it runs the current plan.
- `--timeout` sets how long to wait for `/readyz`, 3 minutes by default. The CVM is usually ready within a minute.

## Config file

```toml
version  = "verirelay-deploy-config/1"
instance = "relay"
release  = "rel-2026.10.30-1"
veriprov = "https://veriprov.example.com"

[machine]
vcpus  = 8
memory = "16G"

[network]
forward = "127.0.0.1:18081"
```

| Key | Value |
| --- | --- |
| `instance` | Name of the instance: 1 to 32 characters of `a-z`, `0-9` and `-`. The unit is `verirelay-<instance>.service`. |
| `release` | Release tag of `verirelay/release`. |
| `veriprov` | HTTPS origin of your VeriProv, as its Bootstraps name it. `fetch` downloads this instance's Bootstrap from there. |
| `profile` | Optional: the profile of the Bootstrap, when your VeriProv serves more than one. Empty selects the one without a profile. |
| `machine.vcpus` | vCPU count. The release must list a Launch Measurement for it; current releases list 1, 2, 4, 8 and 16. |
| `machine.memory` | Guest memory, such as `16G` or `4096M`; at least `512M`. |
| `network.forward` | Host IPv4 address and port forwarded to VeriRelay. Use a loopback address behind your proxy. |

Optional keys, placed before `[machine]`:

| Key | Default | Value |
| --- | --- | --- |
| `qemu` | `/usr/bin/qemu-system-x86_64` | Absolute path of QEMU. |
| `release_repository` | `https://github.com/verirelay/release` | Where releases are downloaded from. Build Records must name this repository. |
| `release_keys` | the official Release Key | Release Keys to trust, as unpadded base64url Ed25519 public keys. Set it when you publish your own releases. |
| `registry` | `https://registry.verirelay.com` | Registry for the revocation lookup. `""` turns the lookup off. |

Unknown keys are errors. Nothing that enters the Launch Measurement or the SNP guest policy can be configured: those come from the release.

The instance directory's path may contain only `A-Z a-z 0-9 . _ / + -`, and only root may be able to change it (see [Host checks](#host-checks)).

## Instance directory

| File | Written by | Content |
| --- | --- | --- |
| `deploy.toml` | you | The config file. |
| `bootstrap.json` | `fetch` | The Bootstrap from your VeriProv, byte for byte. |
| `releases/<tag>/` | `fetch` | `OVMF.fd`, `vmlinuz`, `initrd.img`, `SHA256SUMS`, `chain.json`, `build-records.json`. Place them by hand for an offline host. |
| `config.img` | `up` | The Config Disk: the Bootstrap followed by zero padding to 64 KiB. |
| `plan.json` | `up` | What `up` last launched. `up` restarts the CVM when the plan differs from it, and `status` compares against it. |

The unit `/etc/systemd/system/verirelay-<instance>.service` holds the full QEMU command line, so `systemctl cat` shows exactly what runs. It starts on boot without Deploy. Change `deploy.toml` and run `up` instead of editing the unit.

Several instances can run on one host: give each its own directory, `instance` and `network.forward`. Instances that share a Bootstrap get the same Runtime Config from VeriProv. `plan` and `up` refuse to replace a unit generated from another config file, so two instances with the same name cannot overwrite each other.

Logs: `journalctl -u verirelay-<instance>`.

## Release checks

`fetch`, `plan` and `up` all check:

- the three boot files against `SHA256SUMS`, and `SHA256SUMS` against `chain.json`;
- that `chain.json` is for the configured tag, uses the launch profile `sev-snp-qemu-direct-boot/1`, and lists a Launch Measurement for the vCPU count;
- that `build-records.json` holds a Build Record for that measurement, naming the tag and `release_repository`, with a valid signature by one of `release_keys`.

They also ask Registry about the measurement, and judge the answer as the Verifier's code verdict does:

- A Build Record under one of `release_keys` that Registry marks revoked, or whose signature is invalid, fails the check. The reason is shown when the revocation is signed by one of `release_keys`.
- An unreachable Registry, or an answer with no Build Record under `release_keys`, prints a warning.

## Bootstrap checks

`fetch` downloads the Bootstrap from VeriProv's `GET /v1/bootstraps` and keeps it only when:

- VeriProv serves a Bootstrap with the configured `profile`;
- its bytes hash to the `HOST_DATA` served with them;
- it is a valid Bootstrap that names `veriprov` and `profile`.

When the Bootstrap changed, for example after VeriProv got a new identity key, `fetch` shows the old and new `HOST_DATA`, and `plan` shows the unit's new `host-data`. `plan` and `up` stop when `bootstrap.json` names another VeriProv or profile than `deploy.toml`.

## Host checks

`plan` and `up` stop when any of these fails:

| Check | Passes when |
| --- | --- |
| Files | Only root can change the config file, the Bootstrap, QEMU, the boot files and `config.img`, and every directory above them. QEMU runs as root, so a user who could change these could run their own guest as root. QEMU is not run before this passes. |
| SEV-SNP | `/dev/kvm` and `/dev/sev` exist and `kvm_amd` has SEV-SNP enabled. |
| QEMU | `qemu -object help` lists `sev-snp-guest`. |
| Port | `network.forward` is free, or held by this instance's own unit, launched by `up` with the same address. |
| Memory | `machine.memory` fits the host's memory. |
| VeriProv | The Bootstrap's VeriProv answers `/readyz` over TLS with this host's CA roots. The CVM checks the certificate with its own CA bundle. |
| AMD KDS | `https://kdsintf.amd.com` is reachable. The CVM fetches its VCEK there. |

## Startup failures

When `/readyz` stays unavailable, `up` reads the unit's journal since the restart and names the cause of the most recent known failure. An opened readiness gate outranks every failure:

| Journal shows | Likely cause |
| --- | --- |
| `server installed; readiness gate opened` | Provisioning worked, but the CVM cannot reach the Receipt or attestation store set in the Runtime Config. |
| `policy.denied` | VeriProv's Attestation policy lacks the measurement, or none of its deployments has this `HOST_DATA`. VeriProv's log names which. |
| `bootstrap does not match HOST_DATA` | `config.img` and the unit's `host-data` disagree. Run `up` again. |
| `provision envelope signature rejected` | The Bootstrap was issued by another VeriProv. |
| `invalid bootstrap`, `want exactly one virtio block device` | The CVM cannot read `config.img`. |
| `provisioning unavailable` with `x509:` | The CVM does not trust VeriProv's TLS certificate. |
| `provisioning unavailable` | The CVM cannot reach VeriProv. |
| `fetch AMD KDS resource` | The CVM cannot fetch its VCEK from AMD KDS. |
| `qemu-system-x86_64:` | QEMU failed to start the CVM. |

## Status checks

`status` passes when:

- the unit is active and equals the one `up` installed;
- `/readyz` answers 200;
- a fresh attestation binds the nonce Deploy sent;
- its measurement and `HOST_DATA` equal what `up` launched;
- the Bootstrap it discloses hashes to that `HOST_DATA`.

`status` reads the Attestation Report without checking its AMD signature. VeriProv checks it before provisioning, and your customers check it with the Verifier.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | A check failed. |
| 2 | Usage error, an invalid config file, or `up` or `down` without root. |
