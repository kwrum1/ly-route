# Ly Route

Ly Route 是面向 x86-64 与 ARM64 路由设备的 VPP 出口网关。当前公开发布流水线只生产已完成前端验收并进入固件阶段的 **Gateway**；Orchestrator 源码保留，但按产品计划暂停构建和发布，避免两个产品混入同一固件。

## 下载与产物

每个版本的 [GitHub Releases](https://github.com/kwrum1/ly-route/releases) 同时提供：

| 平台 | 产物 | 用途 |
| --- | --- | --- |
| x86-64 | `ly-route-gateway-*-amd64-4g.img.zst` | 解压后写入 SSD、NVMe、SATA DOM 或 U 盘 |
| x86-64 | `ly-route-gateway-x86_64-autoinstall.iso` | BIOS/UEFI 全自动安装介质 |
| x86-64 | `ly-route-upgrade-gateway-bookworm-amd64.tar.zst` | Web 控制台升级包 |
| ARM64 | `ly-route-gateway-armbian-bookworm-arm64-installer.tar.zst` | 已安装 Armbian Bookworm 的一键安装包 |
| ARM64 | `ly-route-upgrade-gateway-bookworm-arm64.tar.zst` | ARM64 Web 控制台升级包 |

所有产物带独立 `.sha256`，Release 另附统一 `SHA256SUMS` 和 GitHub 构建来源证明。

## x86 安装

IMG 适合烧录：

```bash
zstd -dc ly-route-gateway-*-amd64-4g.img.zst | sudo dd of=/dev/sdX bs=16M conv=fsync status=progress
```

ISO 启动后，在只检测到一个合格目标磁盘时自动安装。多磁盘设备必须在启动参数中指定 `lyroute.target=/dev/nvme0n1`，否则安装器停止，不猜测目标盘。安装过程会在写入前校验内置 IMG，写入后再次校验目标盘。

## Armbian 一键安装

ARM 包安装到现有 **Armbian Bookworm arm64**，保留设备原有 kernel、DTB、U-Boot 和 `/boot`：

```bash
tar --use-compress-program=unzstd -xf ly-route-gateway-armbian-bookworm-arm64-installer.tar.zst
sudo ./install.sh
```

安装完成后连接首个以太网口，默认管理地址为 `https://192.168.88.1/`。默认账号 `admin`，默认密码 `password`，首次登录强制修改密码。

## ARM 设备范围

一键安装包支持满足以下条件的 ARM 设备：

- `aarch64/arm64`，运行 Armbian Bookworm 和 systemd；
- 至少有两个可用以太网逻辑接口，物理口、USB 网卡或已由系统创建的聚合逻辑口均可；
- 存储空间和内存足以运行 VPP、SmartDNS、Kea、Xray 与控制面；
- 网卡驱动保留在 Linux 中，安装器不会预先绑定 VFIO、强制占用网卡或改写板卡启动链。

覆盖的板卡族包括：

- **Rockchip**：RK3328、RK3399、RK3528、RK3566、RK3568、RK3576、RK3582、RK3588/RK3588S；常见设备包括 NanoPi R2S/R2C/R3S/R4S/R5C/R5S/R6C/R6S、Radxa E20C/E25 与 ROCK 3/4/5 系列、Orange Pi 5 系列；
- **Amlogic**：已有 Armbian Bookworm 支持的 arm64 S9xx、A311D 类设备；
- **Allwinner**：已有 Armbian Bookworm 支持并具备足够网口的 H6/H616 类设备；
- 其他能稳定运行 Armbian Bookworm arm64、满足网口和资源条件的设备。

这里的“支持”表示安装层兼容，不等于每块板卡都已完成吞吐和稳定性认证。ARM 不会因为架构被固定锁定数据面：首次配置 LAN/WAN 后，系统与 x86 使用相同能力探测，优先选择通过验证的 VPP 原生高性能路径，DPDK 作为回退。只有 VPP 原生路径、DPDK 等合格路径都不可用时才保持转发锁定；是否解锁取决于具体 SoC、网卡控制器、驱动、IOMMU 和内核能力，而不是只看 `RK3588` 之类的 SoC 名称。

## CI 发布规则

工作流位于 [`.github/workflows/gateway-release.yml`](.github/workflows/gateway-release.yml)：

1. 每个 PR 和 `main` 提交执行 Go、产品隔离、前端打包、rootfs/升级包构建器和敏感信息门禁。
2. x86 runner 构建完整 VPP 网关 rootfs、IMG、自动安装 ISO 和升级包。
3. GitHub ARM64 原生 runner 从固定 VPP 源码提交构建 ARM64 运行时，再生成 Armbian 安装包和升级包。
4. `v*` 标签或手动指定发布标签时，汇总校验并发布同一个 GitHub Release。

固定上游版本：VPP `stable/2510` 指定提交、SmartDNS `Release48.3`、Xray `v26.3.27`。版本升级必须通过代码评审修改，不跟随上游分支漂移。

## 源码结构

- `backend/cmd/gateway-control/`：网关控制面入口
- `backend/internal/`：配置、运行态、VPP、DNS、DHCP、PPPoE、代理与升级逻辑
- `frontend/gateway/`：出口网关前端
- `runtime/`：Ly Route VPP 插件源码
- `packaging/`：rootfs、systemd、nginx 与产品清单
- `scripts/`：验证和生产制品构建器

详细设计与验收边界见 [`docs/`](docs/README.md) 和 [`docs/zh/`](docs/zh/README.md)。
