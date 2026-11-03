English | [简体中文](README.zh-CN.md)

# VeriDeploy

VeriDeploy runs VeriRelay on your AMD SEV-SNP server. It fetches a published release, checks it, launches the CVM under systemd, and confirms that the running CVM attests what was launched. Upgrading or rolling back is one line in a config file. Its command is `verideploy`.

- [CLI reference](docs/cli.md): commands, the config file, checks and exit codes
- [Development](docs/development.md): checks, release builds and tests
- [Security](SECURITY.md): vulnerability reporting and security scope

## What you need

- An AMD EPYC server (Milan or later) with SEV-SNP enabled, and QEMU 9.1 or later. The packages do not pull in QEMU: older distribution QEMU lacks SEV-SNP, and `verideploy plan` checks the one you have.
- A VeriProv (v0.2.0 or later) with a deployment for this CVM. VeriDeploy fetches the deployment's Bootstrap from it, so it can run on another server.

VeriDeploy manages the CVM only. VeriProv with its Attestation policy and Runtime Config, the Receipt store, TLS, and the proxy in front of the CVM stay yours.

## Install

Each [release](https://github.com/verirelay/verideploy/releases) has a `.deb`, an `.rpm` and a plain binary, all listed in `SHA256SUMS`.

Debian and Ubuntu:

```sh
curl -fsSLO https://github.com/verirelay/verideploy/releases/latest/download/verideploy_amd64.deb
curl -fsSLO https://github.com/verirelay/verideploy/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
sudo apt install ./verideploy_amd64.deb
```

- **RHEL, Fedora, SUSE**: download `verideploy_x86_64.rpm` instead and run `sudo dnf install ./verideploy_x86_64.rpm`, or `sudo zypper install` on SUSE.
- **Other distributions**: download `verideploy-linux-amd64` and run `sudo install -m 755 verideploy-linux-amd64 /usr/local/bin/verideploy`.

Every release asset also has a GitHub artifact attestation, which the GitHub CLI checks:

```sh
gh attestation verify verideploy_amd64.deb \
  --repo verirelay/verideploy \
  --signer-workflow verirelay/verideploy/.github/workflows/release.yml
```

To upgrade, install a newer release the same way. To uninstall, run `sudo apt remove verideploy` or `sudo dnf remove verideploy`.

## Start

Pick a directory for the instance, such as `/srv/verirelay/relay`, and create `deploy.toml` there in either of two ways:

```sh
verideploy init              # asks for each value; Enter keeps the default
verideploy init --template   # writes the defaults for you to edit
```

Both fill in the latest release; with `--template`, fill in `veriprov` yourself. The result looks like this:

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

Then, in that directory:

```sh
verideploy fetch    # download the release and the Bootstrap, and verify them
verideploy plan     # check the host and show what up will launch
verideploy up       # install the systemd unit, start the CVM, wait until ready
verideploy status   # compare what the CVM attests with what up launched
```

`plan` prints the Launch Measurement and `HOST_DATA` your VeriProv must accept. Add the measurement to its Attestation policy before `up`. If VeriProv refuses the CVM, `up` reports why.

## Root or not

`verideploy` runs as whichever user you choose:

- **As root**, `up` installs a system unit in `/etc/systemd/system`, and QEMU runs as root.
- **As any other user**, `up` installs that user's own unit (`systemctl --user`) in `~/.config/systemd/user`, and QEMU runs as that user. The user needs access to `/dev/kvm` and `/dev/sev`. For the CVM to start at boot without a login, enable lingering with `loginctl enable-linger`.

`up` records which one it used, so `status` and `down` find the unit whoever runs them.

## Upgrade and roll back

1. Set `release` in `deploy.toml` to the new tag.
2. Run `verideploy fetch` and `verideploy plan`.
3. Allow the new Launch Measurement in VeriProv.
4. Run `verideploy up`, then `verideploy status`.

To roll back, set the previous tag and run `verideploy up` again. The Bootstrap stays the same, so `HOST_DATA` and your VeriProv deployment stay the same too. Remove the old measurement from VeriProv once the new release runs well.

## How a release is checked

`fetch`, `plan` and `up` all check that:

- the boot files match the release's `SHA256SUMS`;
- the release's `build-records.json` holds a Build Record for the chosen Launch Measurement, signed by the Release Key pinned in VeriDeploy. This check runs offline.
- Registry does not mark the release revoked. VeriDeploy reads Registry's answer as the Verifier's code verdict does, so it never launches a release whose Receipts that verdict fails. When Registry cannot be reached, VeriDeploy warns and goes on.

A changed boot file or launch parameter changes the Launch Measurement, so VeriProv refuses the CVM instead of provisioning it.
