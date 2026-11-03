[English](README.md) | 简体中文

# VeriDeploy

VeriDeploy 在你的 AMD SEV-SNP 服务器上运行 VeriRelay。它下载已发布的 release 并校验，用 systemd 启动 CVM，再确认运行中的 CVM 证明的正是启动时的内容。升级或回滚只需改配置文件里的一行。它的命令是 `verideploy`。

- [CLI 参考](docs/cli.zh-CN.md)：命令、配置文件、检查项与退出码
- [开发](docs/development.zh-CN.md)：检查、发布构建与测试
- [安全](SECURITY.md)：漏洞报告与安全范围

## 准备

- 一台开启 SEV-SNP 的 AMD EPYC 服务器（Milan 及之后），QEMU 9.1 及以上。安装包不会自动安装 QEMU：发行版自带的旧版 QEMU 不支持 SEV-SNP，`verideploy plan` 会检查你装的版本。
- 一个 VeriProv（v0.2.0 及以上），其中有这台 CVM 的部署。VeriDeploy 会从它那里获取该部署的引导文件，所以它可以运行在另一台服务器上。

VeriDeploy 只管 CVM。VeriProv 及其远程证明 policy 和运行配置、收据存储、TLS，以及 CVM 前面的代理，都由你自己负责。

## 安装

每个 [release](https://github.com/verirelay/verideploy/releases) 都提供 `.deb`、`.rpm` 和二进制文件，并都列在 `SHA256SUMS` 中。

Debian 和 Ubuntu：

```sh
curl -fsSLO https://github.com/verirelay/verideploy/releases/latest/download/verideploy_amd64.deb
curl -fsSLO https://github.com/verirelay/verideploy/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
sudo apt install ./verideploy_amd64.deb
```

- **RHEL、Fedora、SUSE**：改为下载 `verideploy_x86_64.rpm`，运行 `sudo dnf install ./verideploy_x86_64.rpm`，SUSE 上用 `sudo zypper install`。
- **其他发行版**：下载 `verideploy-linux-amd64`，运行 `sudo install -m 755 verideploy-linux-amd64 /usr/local/bin/verideploy`。

每个 release 资产还带有 GitHub artifact attestation，可用 GitHub CLI 验证：

```sh
gh attestation verify verideploy_amd64.deb \
  --repo verirelay/verideploy \
  --signer-workflow verirelay/verideploy/.github/workflows/release.yml
```

升级时，用同样的方式安装新的 release。卸载用 `sudo apt remove verideploy` 或 `sudo dnf remove verideploy`。

## 开始

为实例选一个目录，例如 `/srv/verirelay/relay`，在其中用以下任一方式生成 `deploy.toml`：

```sh
verideploy init              # 逐项询问，直接回车即采用默认值
verideploy init --template   # 写入默认值，之后自己修改
```

两种方式都会自动填入最新的 release；用 `--template` 时，`veriprov` 需要自己填写。生成的文件如下：

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

然后在这个目录中运行：

```sh
verideploy fetch    # 下载 release 和引导文件，并完成校验
verideploy plan     # 检查宿主机，显示 up 将要启动的内容
verideploy up       # 安装 systemd unit，启动 CVM，等待就绪
verideploy status   # 比对 CVM 的证明与 up 启动的内容
```

`plan` 会打印你的 VeriProv 需要接受的启动度量和 `HOST_DATA`。请在 `up` 之前把这个启动度量加进 VeriProv 的远程证明 policy。如果 VeriProv 拒绝了 CVM，`up` 会给出原因。

## 用不用 root

`verideploy` 以你选择的用户身份运行：

- **以 root 运行**时，`up` 在 `/etc/systemd/system` 安装系统级 unit，QEMU 以 root 身份运行。
- **以其他用户运行**时，`up` 在 `~/.config/systemd/user` 安装该用户自己的 unit（`systemctl --user`），QEMU 以这个用户身份运行。这个用户需要能访问 `/dev/kvm` 和 `/dev/sev`。要让 CVM 不用登录也能开机自启，请用 `loginctl enable-linger` 开启 lingering。

`up` 会记下用的是哪一种，所以无论谁来运行 `status` 和 `down`，都能找到这个 unit。

## 升级与回滚

1. 把 `deploy.toml` 里的 `release` 改成新的 tag。
2. 运行 `verideploy fetch` 和 `verideploy plan`。
3. 在 VeriProv 中允许新的启动度量。
4. 运行 `verideploy up`，再运行 `verideploy status`。

回滚时，把 tag 改回上一个版本，再运行一次 `verideploy up`。引导文件不变，所以 `HOST_DATA` 和 VeriProv 里的部署也不变。新版本运行稳定后，再从 VeriProv 中删掉旧的启动度量。

## release 怎么校验

`fetch`、`plan` 和 `up` 都会检查：

- 启动产物与 release 的 `SHA256SUMS` 一致；
- release 的 `build-records.json` 中有所选启动度量的构建记录，并由 VeriDeploy 内置的 Release Key 签名。这一步离线完成。
- Registry 没有把这个 release 标记为撤销。VeriDeploy 按 Verifier 代码判定的同一规则解读 Registry 的结果，所以不会启动收据会被该判定判为失败的 release。Registry 连不上时，VeriDeploy 给出警告后继续。

启动产物或启动参数一旦被改动，启动度量就会变化，VeriProv 会拒绝这台 CVM，不会给它下发配置。
