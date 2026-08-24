# 固件组装边界 / Firmware Assembly Boundary

## 中文

x86 发行物以可交互安装 ISO 为唯一全新安装入口，升级使用 amd64 升级包。ARM64
发行物以 Armbian Bookworm 一键安装包为入口，升级使用 arm64 升级包。CI 可生成
临时 rootfs 供组装和检查，但不得把通用 ARM rootfs、IMG 或设备专用镜像上传为
正式产物。

固件必须包含 Web 前端、Gateway API、VPP、Ly Route VPP 插件、SmartDNS、Kea、
Xray、Nginx、systemd 单元、默认配置和恢复脚本。缺少任一运行依赖时构建失败，不能
用空文件或可选校验掩盖。网卡身份持久化使用 MAC/PCI 地址，不依赖 `eth0` 顺序。

## English

The x86 fresh-install artifact is the interactive ISO; upgrades use the amd64
upgrade package. ARM64 uses an Armbian Bookworm one-click installer and arm64
upgrade. Temporary rootfs output may be assembled in CI but generic ARM rootfs,
IMG files and board-specific images are not release artifacts.

Firmware includes the web UI, Gateway API, VPP, Ly Route plugins, SmartDNS,
Kea, Xray, Nginx, systemd units, defaults and recovery scripts. Missing runtime
dependencies fail the build. Persistent interface identity uses MAC/PCI data,
not volatile `eth0` ordering.
