[English](cli.md) | 简体中文

# CLI 参考

## 命令

```sh
verideploy init   [-c FILE] [--template]
verideploy fetch  [-c FILE]
verideploy plan   [-c FILE]
verideploy up     [-c FILE] [--restart] [--timeout DUR]
verideploy down   [-c FILE]
verideploy status [-c FILE]
verideploy version
```

`-c` 指定实例的配置文件，默认是 `deploy.toml`。配置文件所在目录就是实例目录。

| 命令 | 作用 |
| --- | --- |
| `init` | 逐项询问后写出新的配置文件。不会覆盖已有文件。 |
| `fetch` | 把 release 下载到 `releases/<tag>/` 并校验。已存在且通过校验的 release 保持原样；否则新下载的内容通过校验后才会替换它。之后从 VeriProv 获取引导文件，存为 `bootstrap.json`。 |
| `plan` | 校验 release 和宿主机，打印 `up` 将要启动的内容：尚未安装 unit 时显示完整的 unit，否则显示与已安装 unit 的差异。不做任何改动。 |
| `up` | 先做 `plan` 的全部检查，再写入 `config.img` 和 systemd unit。CVM 运行的不是当前 plan 时重启它，然后等待 `/readyz`。任何检查未通过时不做改动。 |
| `down` | 停止并禁用 CVM。文件都会保留。 |
| `status` | 检查 unit 和 `/readyz`，请求一份新的证明，并与 `up` 启动的内容比对。 |

`init` 在方括号中显示每一项的默认值，直接回车即采用。加上 `--template` 时不询问，直接写入默认值，之后由你自己修改：

| 值 | 默认值 |
| --- | --- |
| `instance` | 目录名（是合法实例名时），否则为 `relay` |
| `release` | 从 GitHub 查到的 `verirelay/release` 最新 release |
| `veriprov` | 无：`init` 会询问，`--template` 留空 |
| `profile` | VeriProv 只提供一个引导文件时就用它；有多个时 `init` 会询问 |
| `machine.vcpus`、`machine.memory` | `8`、`16G` |
| `network.forward` | `127.0.0.1` 上从 18081 起第一个空闲端口 |

`init` 会列出你的 VeriProv 提供的引导文件，并显示所选引导文件的 `HOST_DATA`。VeriProv 连不上时，`init` 保留你填的地址并给出警告，`fetch` 时会再试。查不到最新 release 时，`init` 会请你手动输入，`--template` 则留空。

`up` 的选项：

- `--restart`：即使 CVM 运行的就是当前 plan 也重启它。
- `--timeout`：等待 `/readyz` 的时长，默认 3 分钟。CVM 通常在一分钟内就绪。

## 配置文件

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

| 键 | 值 |
| --- | --- |
| `instance` | 实例名：1 到 32 个 `a-z`、`0-9`、`-` 字符。unit 名为 `verirelay-<instance>.service`。 |
| `release` | `verirelay/release` 的 release tag。 |
| `veriprov` | 你的 VeriProv 的 HTTPS origin，与它的引导文件中写的一致。`fetch` 从这里下载本实例的引导文件。 |
| `profile` | 可选：VeriProv 提供多个引导文件时，所用引导文件的 profile。留空则选没有 profile 的那个。 |
| `machine.vcpus` | vCPU 数。release 必须列出它对应的启动度量；当前的 release 包含 1、2、4、8 和 16。 |
| `machine.memory` | 内存，例如 `16G` 或 `4096M`，最少 `512M`。 |
| `network.forward` | 转发到 VeriRelay 的宿主机 IPv4 地址和端口。放在代理后面时，用 loopback 地址。 |

可选键，写在 `[machine]` 之前：

| 键 | 默认值 | 值 |
| --- | --- | --- |
| `qemu` | `/usr/bin/qemu-system-x86_64` | QEMU 的绝对路径。 |
| `release_repository` | `https://github.com/verirelay/release` | 下载 release 的仓库。构建记录必须写明这个仓库。 |
| `release_keys` | 官方 Release Key | 信任的 Release Key，格式为无填充 base64url 的 Ed25519 公钥。自己发布 release 时设置。 |
| `registry` | `https://registry.verirelay.com` | 查询撤销状态的 Registry。设为 `""` 则不查询。 |

未知的键会报错。凡是进入启动度量或 SNP guest policy 的参数都不能配置，它们只来自 release。

实例目录的路径只能包含 `A-Z a-z 0-9 . _ / + -`，因为它会出现在 QEMU 参数和 unit 中。

## 实例目录

| 文件 | 写入者 | 内容 |
| --- | --- | --- |
| `deploy.toml` | 你 | 配置文件。 |
| `bootstrap.json` | `fetch` | 从 VeriProv 获取的引导文件，与原始字节完全一致。 |
| `releases/<tag>/` | `fetch` | `OVMF.fd`、`vmlinuz`、`initrd.img`、`SHA256SUMS`、`chain.json`、`build-records.json`。离线的宿主机可以手动放入。 |
| `config.img` | `up` | 配置盘：引导文件后补零到 64 KiB。 |
| `plan.json` | `up` | `up` 上一次启动的内容。plan 与它不同时 `up` 会重启 CVM，`status` 也以它为准比对。 |

unit `verirelay-<instance>.service` 写有完整的 QEMU 命令行，`systemctl cat` 看到的就是实际运行的命令。它开机自启，不依赖 VeriDeploy。需要修改时，改 `deploy.toml` 后运行 `up`，不要直接改 unit。

一台宿主机上可以运行多个实例：每个实例用自己的目录、`instance` 和 `network.forward`。共用同一个引导文件的实例，会从 VeriProv 拿到同一份运行配置。`plan` 和 `up` 不会替换由其他配置文件生成的 unit，所以同名的两个实例不会互相覆盖。

## systemd manager

unit 装在哪里，取决于由谁运行 `plan` 和 `up`：

| 运行身份 | unit | QEMU 的运行身份 | 日志 |
| --- | --- | --- | --- |
| root | `/etc/systemd/system/verirelay-<instance>.service` | root | `journalctl -u verirelay-<instance>` |
| 其他用户 | `~/.config/systemd/user/verirelay-<instance>.service`（设置了 `$XDG_CONFIG_HOME` 时以它为准），由 `systemctl --user` 管理 | 该用户 | `journalctl --user -u verirelay-<instance>` |

以普通用户运行时，QEMU 需要能访问 `/dev/kvm` 和 `/dev/sev`。用户级 unit 只有在开启 lingering（`loginctl enable-linger`）后才会开机自启。如果 shell 中没有 `XDG_RUNTIME_DIR`（例如通过 `sudo -u <用户> -i` 或 `su` 切换过来），VeriDeploy 会使用 `/run/user/<uid>` 连接该用户的 systemd。

`up` 会把所用的 manager 记进 `plan.json`；之后无论谁运行 `status` 和 `down`，都按记录的那个来操作。

## release 校验

`fetch`、`plan` 和 `up` 都会检查：

- 三个启动产物与 `SHA256SUMS` 一致，`SHA256SUMS` 与 `chain.json` 一致；
- `chain.json` 属于配置的 tag，使用启动配置 `sev-snp-qemu-direct-boot/1`，并列出该 vCPU 数的启动度量；
- `build-records.json` 中有该启动度量的构建记录，写明这个 tag 和 `release_repository`，并带有 `release_keys` 中某个 key 的有效签名。

它们还会向 Registry 查询这个启动度量，并按 Verifier 代码判定的同一规则解读结果：

- `release_keys` 签名的构建记录被 Registry 标记为撤销，或者签名无效，检查就失败。撤销声明由 `release_keys` 签名时，会显示撤销原因。
- Registry 连不上，或者返回的结果中没有 `release_keys` 签名的构建记录，只给出警告。

## 引导文件检查

`fetch` 从 VeriProv 的 `GET /v1/bootstraps` 下载引导文件，满足以下全部条件才保存：

- VeriProv 提供了 `profile` 对应的引导文件；
- 它的字节哈希等于随之返回的 `HOST_DATA`；
- 它是合法的引导文件，并且写明的就是 `veriprov` 和 `profile`。

引导文件有变化时（例如 VeriProv 换了身份密钥），`fetch` 会显示新旧 `HOST_DATA`，`plan` 会显示 unit 中新的 `host-data`。`bootstrap.json` 写明的 VeriProv 或 profile 与 `deploy.toml` 不一致时，`plan` 和 `up` 会停止。

## 宿主机检查

`plan` 和 `up` 遇到任何一项失败都会停止：

| 检查 | 通过条件 |
| --- | --- |
| SEV-SNP | `/dev/kvm` 和 `/dev/sev` 存在，且 `kvm_amd` 开启了 SEV-SNP。 |
| QEMU | `qemu -object help` 列出 `sev-snp-guest`。 |
| Port | `network.forward` 空闲，或者被本实例自己的 unit 占用，且 `up` 启动它时用的是同一个地址和同一个 manager。 |
| Memory | `machine.memory` 不超过宿主机内存。 |
| VeriProv | 引导文件中的 VeriProv 用本机 CA 根证书通过 TLS 访问时，`/readyz` 返回成功。CVM 用自己的 CA bundle 校验证书。 |
| AMD KDS | 能访问 `https://kdsintf.amd.com`。CVM 从这里获取 VCEK。 |

## 启动失败

`/readyz` 一直不可用时，`up` 会读取 unit 自重启以来的日志，给出最近一次能识别的失败原因。只要出现过就绪门已打开的日志，就以它为准：

| 日志中出现 | 可能的原因 |
| --- | --- |
| `server installed; readiness gate opened` | 配置下发成功，但 CVM 连不上运行配置里的收据或证明存储。 |
| `policy.denied` | VeriProv 的远程证明 policy 里没有这个启动度量，或者没有任何部署的 `HOST_DATA` 与之相符。VeriProv 的日志会写明是哪一种。 |
| `bootstrap does not match HOST_DATA` | `config.img` 与 unit 中的 `host-data` 不一致。再运行一次 `up`。 |
| `provision envelope signature rejected` | 引导文件是另一个 VeriProv 签发的。 |
| `invalid bootstrap`、`want exactly one virtio block device` | CVM 读不到 `config.img`。 |
| `provisioning unavailable` 且含 `x509:` | CVM 不信任 VeriProv 的 TLS 证书。 |
| `provisioning unavailable` | CVM 连不上 VeriProv。 |
| `fetch AMD KDS resource` | CVM 无法从 AMD KDS 获取 VCEK。 |
| `Failed to open /dev/sev`、`Could not access KVM kernel module` | 运行 unit 的用户打不开 `/dev/sev` 或 `/dev/kvm`。 |
| `qemu-system-x86_64:` | QEMU 未能启动 CVM。`up` 会显示 QEMU 最后输出的全部报错：最后一行往往是笼统的报错，真正的原因在它前面。 |

## 状态检查

`status` 在以下条件都满足时通过：

- unit 处于 active，且与 `up` 安装的一致；
- `/readyz` 返回 200；
- 新取得的证明绑定了 VeriDeploy 发送的 nonce；
- 证明中的启动度量和 `HOST_DATA` 与 `up` 启动的一致；
- 证明中披露的引导文件的哈希等于该 `HOST_DATA`。

`status` 读取证明报告，但不校验它的 AMD 签名。VeriProv 在下发配置前会校验，你的客户则用 Verifier 校验。

## 退出码

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功。 |
| 1 | 有检查未通过。 |
| 2 | 用法错误或配置文件无效。 |
