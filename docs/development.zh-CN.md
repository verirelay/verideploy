[English](development.md) | 简体中文

# 开发

## 检查

```sh
go run mvdan.cc/gofumpt@v0.12.0 -w .
go vet ./...
go test ./...
make build
golangci-lint run
actionlint
```

CI 使用 golangci-lint v2.15.0 和 actionlint v1.7.12。`make release` 调用 nfpm v2.48.0，CI 会安装并卸载它打出的 `.deb`。

| Target | 作用 |
| --- | --- |
| `make build` | 构建 `dist/verideploy` |
| `make check` | 运行测试、构建和 lint |
| `make release VERSION=<tag>` | 把 `verideploy-linux-amd64`、它的 `.deb` 和 `.rpm`（由 nfpm 按 `nfpm.yaml` 打包）以及 `SHA256SUMS` 构建到 `dist/release` |

推送 `v*` tag 会触发 release workflow，它会：

1. 运行 `make release`；
2. 为 `dist/release` 中的每个资产生成 GitHub artifact attestation；
3. 发布 GitHub Release。

## 测试

测试不需要 SEV-SNP 硬件，宿主机目录树、命令执行、VeriProv 和 CVM 都由假实现代替。

| Fixture | 内容 |
| --- | --- |
| `internal/release/testdata/rel-2026.10.30-1/` | 一个已发布 release 的 `chain.json`、`build-records.json` 和 `SHA256SUMS`。其中的构建记录必须能用官方 Release Key 验证通过。 |
| `internal/plan/testdata/od3-verirelay.service` | 一个手工部署的生产 unit。推导出的 QEMU 命令行除文件路径外必须与它一致。 |
| `internal/plan/testdata/verirelay-relay.service` | VeriDeploy 为同一实例生成的 unit。 |

审阅后更新生成的 unit：

```sh
go test ./internal/plan -run TestProductionArgv -update
```
