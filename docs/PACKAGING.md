# 打包与 CI

本仓库的 CI **不编译产品源码**，也不包含产品源码。它把已发布的二进制输入组装成
可安装的产物，并附加到 GitHub Release：

- x86_64：安装 ISO；
- ARM64：Armbian Bookworm 一键安装包。

## 输入

| 输入 | 说明 |
| --- | --- |
| x86 升级包 | `ly-route-upgrade-gateway-bookworm-amd64.tar.zst` |
| 基础 ISO | 上一正式版的 `ly-route-gateway-gui-installer.iso`，提供 Live 启动骨架与基础 rootfs |
| ARM 升级包 | `ly-route-upgrade-gateway-bookworm-arm64.tar.zst` |
| 基础 ARM 安装包 | 上一正式版的 `ly-route-gateway-arm64-installer.tar.zst`，提供 overlay 与 packages |
| 版本号 | 目标版本，例如 `1.1.4`，写入 `etc/ly-route/version.env` |

基础 ISO / 基础 ARM 安装包只作为骨架；升级包提供目标版本的控制面、VPP 插件、Web UI、
服务单元与配置模板。组装过程不访问产品源码。

## x86_64：本地组装 ISO

```bash
sudo apt-get install -y xorriso zstd

packaging/build-gateway-iso-from-base.sh \
  --version 1.1.4 \
  --package ly-route-upgrade-gateway-bookworm-amd64.tar.zst \
  --base-iso ly-route-gateway-gui-installer.iso \
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

## ARM64：本地组装一键安装包

```bash
packaging/build-arm64-installer-from-base.sh \
  --version 1.1.4 \
  --package ly-route-upgrade-gateway-bookworm-arm64.tar.zst \
  --base-installer ly-route-gateway-arm64-installer.tar.zst \
  --out dist \
  --commit "$(git rev-parse HEAD)"
```

输出：

```text
dist/ly-route-gateway-arm64-installer.tar.zst
dist/ly-route-gateway-arm64-installer.tar.zst.sha256
```

脚本会：

1. 解包基础 ARM 安装包（`overlay/`、`overlay.tar.zst`、`packages/`、`install.sh`）；
2. 把目标版本的 ARM 升级包覆盖到 `overlay/`；
3. 更新 `version.env`、`product-manifest.json` 与 `install.sh` 的版本；
4. 重新生成 `overlay.tar.zst`、`manifest.json` 与 `checksums.sha256`；
5. 重新打包安装包并校验版本、插件与校验和。

## GitHub Actions

工作流：`.github/workflows/release.yml`

触发方式：

- 手动 `workflow_dispatch`：私有流水线上传升级包后触发；
- 不使用 tag push 自动触发，避免 Release 资产尚未上传时误跑。

手动参数：

| 参数 | 说明 |
| --- | --- |
| `version` | 目标版本，例如 `1.1.4` |
| `base_version` | 基础产物的 Release 标签，例如 `v1.2` |
| `package_url` | x86 升级包直链；留空则从目标版本 Release 下载 |
| `arm_package_url` | ARM64 升级包直链；留空则从目标版本 Release 下载 |

任务顺序：

1. `gateway-iso`：下载基础 ISO 与 x86 升级包，组装 ISO，创建/更新 Release；
2. `gateway-arm64`：在 ISO 任务成功后，下载基础 ARM 安装包与 ARM 升级包，组装
   新 ARM 安装包；
3. ARM 任务更新同一个 `SHA256SUMS`，保留 x86 与其他资产条目；
4. 所有任务上传 ISO / ARM 安装包 / ARM 升级包 / 校验文件到 `v<version>` Release。

## 发布约定

- 私有构建流水线先创建目标版本 Release，并上传升级包；
- GitHub Actions 随后补齐 x86_64 ISO 与 ARM64 安装包；
- 所有发布资产都应附带 `SHA256SUMS`；
- 不要在公开仓库提交源码、密钥、内部构建日志或私有补丁。

## 资源需求

GitHub 托管 runner（`ubuntu-latest`）实测资源需求：

- x86 ISO：磁盘约 6–8 GB，10–20 分钟；
- ARM 安装包：磁盘约 1–2 GB，1–3 分钟；
- 内存：2 GB 以上。

如果 runner 磁盘不足，可改用自托管 runner；工作流本身不依赖特定主机。
