# Ly Route Rootfs 与固件构建

语言：[English](../rootfs-image.md) | 简体中文

本仓库分别构建 Gateway 与 Orchestrator 的 Debian rootfs、x86 物理机可写盘镜像和 ARM 固件。构建时必须显式选择产品 profile，产物只能包含该产品的服务、API/资源、UI、数据库与迁移，禁止运行时产品互转。Gateway 的首启 LAN/DHCP 默认值不适用于使用独立 Linux 管理口的 Orchestrator。

目标产物同时包含获准的 VPP 原生高性能插件和受控回退所需的 VPP DPDK 插件，由自动选择器决定实际使用路径。当前制品验证器仍拒绝 DPDK，必须完成工作计划 D1 后才能宣称 DPDK 可用。AF_XDP copy、Linux generic XDP、AF_PACKET 和 Linux 转发回退继续禁止进入产物。

## 本地 rootfs

在 Debian/Ubuntu 构建机安装依赖：

```sh
sudo apt-get update
sudo apt-get install -y mmdebstrap qemu-user-static binfmt-support zstd ca-certificates golang-go
```

构建 rootfs：

```sh
make rootfs ARCH=amd64
make rootfs ARCH=arm64
```

如果需要注入运行态包，可以先构建本地 `.deb`：

```sh
make runtime-debs
LY_ROUTE_EXTRA_DEBS_DIR=/root/ly-route/runtime-debs make rootfs ARCH=amd64
```

rootfs 产物写入 `dist/rootfs/`，文件名为 `ly-route-rootfs-bookworm-<arch>.tar.zst` 及对应 `.sha256`。

## x86 物理机镜像

从已生成的 amd64 rootfs 构建 4 GiB 可写盘镜像：

```sh
sudo ./scripts/build-disk-image.sh \
  --rootfs dist/rootfs/ly-route-rootfs-bookworm-amd64.tar.zst \
  --out dist/disk-image-current \
  --size 4G
```

产物包括 `.img`、`.img.sha256`、`.img.zst` 和 `.img.zst.sha256`。写盘时必须写入整块磁盘，不能写入分区。

## ARM / ophub Armbian 固件

从 arm64 rootfs 构建 ophub/Armbian 固件：

```sh
sudo ./scripts/build-ophub-armbian-images.sh \
  --rootfs dist/rootfs/ly-route-rootfs-bookworm-arm64.tar.zst \
  --boards all \
  --out dist/ophub-armbian
```

`LY_ROUTE_OPHUB_BOARDS` 直接对应 ophub `rebuild -b` 参数，例如 `all`、`amlogic`、`rockchip`、`allwinner`、`s905x3`、`rk3588` 或具体设备名。`LY_ROUTE_OPHUB_BASE_IMAGE_URL` 可固定基础 Armbian 镜像；未设置时会从 GitHub Release 获取最新 ophub trunk arm64 镜像。

## CI 与 Release

Gitea `.gitea/workflows/ci.yml` 在 push、pull request、手动触发和每周一 04:00 Asia/Shanghai 运行。它验证脚本、Go 后端、契约和轻量 rootfs，但不发布固件。

GitHub `.github/workflows/firmware.yml` 负责重型固件构建和发布：

- `ly-route-x86-physical-*`：x86 物理机磁盘镜像。
- `ly-route-armbian-*`：Amlogic、Rockchip、Allwinner 批次 ARM 镜像。

旧的自定义 Rockchip 本地构建器仅保留用于定向实验；生产 ARM 固件以 GitHub ophub 工作流为准。

## 默认访问

镜像首次启动后，第一个非 loopback 以太网接口作为 LAN 管理口，地址为 `192.168.88.1/24`，Kea DHCP 地址池为 `192.168.88.100-192.168.88.199`，管理 UI 位于 `https://192.168.88.1/`。默认账号为 `admin` / `admin12345`，首次登录后必须修改密码。
