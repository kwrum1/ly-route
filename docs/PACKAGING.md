# 打包与 CI

本仓库的 CI **不编译产品源码**，也不包含产品源码。它只把已发布的二进制输入组装成
可安装的 ISO，并附加到 GitHub Release。

## 输入

| 输入 | 说明 |
| --- | --- |
| 升级包 | `ly-route-upgrade-gateway-bookworm-amd64.tar.zst`，由私有构建流水线产出 |
| 基础 ISO | 上一正式版的 `ly-route-gateway-gui-installer.iso`，提供 Live 启动骨架与基础 rootfs |
| 版本号 | 目标版本，例如 `1.1.4`，写入 `etc/ly-route/version.env` |

基础 ISO 只作为启动与安装器骨架；升级包提供目标版本的控制面、VPP 插件、Web UI、
服务单元与配置模板。组装过程不访问产品源码。

## 本地组装

```bash
sudo apt-get install -y xorriso zstd

packaging/build-gateway-iso-from-base.sh \
  --version 1.1.4 \
  --package ly-route-upgrade-gateway-bookworm-amd64.tar.zst \
  --base-iso ly-route-gateway-gui-installer-1.1.3.iso \
  --out dist
```

输出：

```text
dist/ly-route-gateway-gui-installer.iso
dist/ly-route-gateway-gui-installer.iso.sha256
```

脚本会：

1. 从基础 ISO 提取 `/installer/payload/rootfs.tar.zst`；
2. 解包 rootfs，覆盖升级包内容；
3. 删除不属于 Gateway 的编排器插件与临时文件；
4. 写入 `LY_ROUTE_VERSION`，并把 `LY_ROUTE_DPDK_TRANSPORT` 固定为 `uio`；
5. 重新压缩 payload，更新 `.sha256` / `.size` 边车文件；
6. 用 xorriso 替换 ISO 内的 payload，保留原有 BIOS/UEFI 启动结构。

## GitHub Actions

工作流：`.github/workflows/release.yml`

触发方式：

- 手动 `workflow_dispatch`：私有流水线上传升级包后触发；
- 不使用 tag push 自动触发，避免 Release 资产尚未上传时误跑。

手动参数：

| 参数 | 说明 |
| --- | --- |
| `version` | 目标版本，例如 `1.1.4` |
| `base_version` | 基础 ISO 的 Release 标签，例如 `v1.1.3` |
| `package_url` | 升级包直链；留空则从目标版本 Release 下载 |

流程：

1. 下载基础 ISO；
2. 下载目标版本的升级包（或 `package_url`）；
3. 运行 `packaging/build-gateway-iso-from-base.sh`；
4. 校验 ISO 内 payload 的 SHA256 与 `version.env`；
5. 生成 `SHA256SUMS`；
6. 创建或更新 `v<version>` Release，上传 ISO 与校验文件。

## 发布约定

- 私有构建流水线先创建目标版本 Release，并上传升级包与 ARM64 产物；
- GitHub Actions 随后补齐 x86_64 ISO；
- 所有发布资产都应附带 `SHA256SUMS`；
- 不要在公开仓库提交源码、密钥、内部构建日志或私有补丁。

## 资源需求

GitHub 托管 runner（`ubuntu-latest`）实测资源需求：

- 磁盘：约 6–8 GB（基础 ISO + 解包 rootfs + 新 ISO）；
- 时间：约 10–20 分钟；
- 内存：2 GB 以上。

如果 runner 磁盘不足，可改用自托管 runner；工作流本身不依赖特定主机。
