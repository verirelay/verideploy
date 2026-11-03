[English](README.md) | 简体中文

# Deploy

Deploy 在你的 AMD SEV-SNP 服务器上运行 VeriRelay。它下载已发布的 release 并校验，用 systemd 启动 CVM，再确认运行中的 CVM 证明的正是启动时的内容。升级或回滚只需改配置文件里的一行。

- [CLI 参考](docs/cli.zh-CN.md)：命令、配置文件、检查项与退出码
- [开发](docs/development.zh-CN.md)：检查、发布构建与测试
- [安全](SECURITY.md)：漏洞报告与安全范围

## 准备

- 一台开启 SEV-SNP 的 AMD EPYC 服务器（Milan 及之后），QEMU 9.1 及以上。
- 一个 VeriProv（v0.2.0 及以上），其中有这台 CVM 的部署。Deploy 会从它那里获取该部署的引导文件，所以它可以运行在另一台服务器上。

Deploy 只管 CVM。VeriProv 及其远程证明 policy 和运行配置、收据存储、TLS，以及 CVM 前面的代理，都由你自己负责。

## 开始

从 [Releases](https://github.com/verirelay/deploy/releases) 下载 `deploy-linux-amd64`，并用 `SHA256SUMS` 核对。每个 release 资产都带有 GitHub artifact attestation：

```sh
gh attestation verify deploy-linux-amd64 \
  --repo verirelay/deploy \
  --signer-workflow verirelay/deploy/.github/workflows/release.yml
```

QEMU 以 root 身份运行，所以实例目录必须只有 root 能改动，例如 `/srv/verirelay/relay`。在其中用以下任一方式生成 `deploy.toml`：

```sh
sudo deploy init              # 逐项询问，直接回车即采用默认值
sudo deploy init --template   # 写入默认值，之后自己修改
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
sudo deploy fetch   # 下载 release 和引导文件，并完成校验
deploy plan         # 检查宿主机，显示 up 将要启动的内容
sudo deploy up      # 安装 systemd unit，启动 CVM，等待就绪
deploy status       # 比对 CVM 的证明与 up 启动的内容
```

`plan` 会打印你的 VeriProv 需要接受的启动度量和 `HOST_DATA`。请在 `up` 之前把这个启动度量加进 VeriProv 的远程证明 policy。如果 VeriProv 拒绝了 CVM，`up` 会给出原因。

## 升级与回滚

1. 把 `deploy.toml` 里的 `release` 改成新的 tag。
2. 运行 `sudo deploy fetch` 和 `deploy plan`。
3. 在 VeriProv 中允许新的启动度量。
4. 运行 `sudo deploy up`，再运行 `deploy status`。

回滚时，把 tag 改回上一个版本，再运行一次 `sudo deploy up`。引导文件不变，所以 `HOST_DATA` 和 VeriProv 里的部署也不变。新版本运行稳定后，再从 VeriProv 中删掉旧的启动度量。

## release 怎么校验

`fetch`、`plan` 和 `up` 都会检查：

- 启动产物与 release 的 `SHA256SUMS` 一致；
- release 的 `build-records.json` 中有所选启动度量的构建记录，并由 Deploy 内置的 Release Key 签名。这一步离线完成。
- Registry 没有把这个 release 标记为撤销。Deploy 按 Verifier 代码判定的同一规则解读 Registry 的结果，所以不会启动收据会被该判定判为失败的 release。Registry 连不上时，Deploy 给出警告后继续。

启动产物或启动参数一旦被改动，启动度量就会变化，VeriProv 会拒绝这台 CVM，不会给它下发配置。
