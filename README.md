English | [简体中文](README.zh-CN.md)

# Deploy

Deploy runs VeriRelay on your AMD SEV-SNP server. It fetches a published release, checks it, launches the CVM under systemd, and confirms that the running CVM attests what was launched. Upgrading or rolling back is one line in a config file.

- [CLI reference](docs/cli.md): commands, the config file, checks and exit codes
- [Development](docs/development.md): checks, release builds and tests
- [Security](SECURITY.md): vulnerability reporting and security scope

## What you need

- An AMD EPYC server (Milan or later) with SEV-SNP enabled, and QEMU 9.1 or later.
- A VeriProv (v0.2.0 or later) with a deployment for this CVM. Deploy fetches the deployment's Bootstrap from it, so it can run on another server.

Deploy manages the CVM only. VeriProv with its Attestation policy and Runtime Config, the Receipt store, TLS, and the proxy in front of the CVM stay yours.

## Start

Download `deploy-linux-amd64` from [Releases](https://github.com/verirelay/deploy/releases) and check it against `SHA256SUMS`. Every release asset has a GitHub artifact attestation:

```sh
gh attestation verify deploy-linux-amd64 \
  --repo verirelay/deploy \
  --signer-workflow verirelay/deploy/.github/workflows/release.yml
```

QEMU runs as root, so the instance directory must be one only root can change, such as `/srv/verirelay/relay`. Create `deploy.toml` there in either of two ways:

```sh
sudo deploy init              # asks for each value; Enter keeps the default
sudo deploy init --template   # writes the defaults for you to edit
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
sudo deploy fetch   # download the release and the Bootstrap, and verify them
deploy plan         # check the host and show what up will launch
sudo deploy up      # install the systemd unit, start the CVM, wait until ready
deploy status       # compare what the CVM attests with what up launched
```

`plan` prints the Launch Measurement and `HOST_DATA` your VeriProv must accept. Add the measurement to its Attestation policy before `up`. If VeriProv refuses the CVM, `up` reports why.

## Upgrade and roll back

1. Set `release` in `deploy.toml` to the new tag.
2. Run `sudo deploy fetch` and `deploy plan`.
3. Allow the new Launch Measurement in VeriProv.
4. Run `sudo deploy up`, then `deploy status`.

To roll back, set the previous tag and run `sudo deploy up` again. The Bootstrap stays the same, so `HOST_DATA` and your VeriProv deployment stay the same too. Remove the old measurement from VeriProv once the new release runs well.

## How a release is checked

`fetch`, `plan` and `up` all check that:

- the boot files match the release's `SHA256SUMS`;
- the release's `build-records.json` holds a Build Record for the chosen Launch Measurement, signed by the Release Key pinned in Deploy. This check runs offline.
- Registry does not mark the release revoked. Deploy reads Registry's answer as the Verifier's code verdict does, so it never launches a release whose Receipts that verdict fails. When Registry cannot be reached, Deploy warns and goes on.

A changed boot file or launch parameter changes the Launch Measurement, so VeriProv refuses the CVM instead of provisioning it.
