# 发布与安装 / Release And Installation

## 中文

每次 `main` 完整构建成功后，CI 自动创建版本号为 `v0.1.<run_number>` 的 GitHub
Release；标签构建和手动构建可指定符合语义化格式的版本。Release 固定包含：

- `ly-route-gateway-<version>-x86_64-installer.iso`
- `ly-route-gateway-<version>-upgrade-amd64.tar.zst`
- `ly-route-gateway-<version>-armbian-arm64-installer.tar.zst`
- `ly-route-gateway-<version>-upgrade-arm64.tar.zst`
- `SHA256SUMS`

安装前必须校验 `sha256sum -c SHA256SUMS`。x86 从 ISO 启动，按界面选择安装磁盘、
管理网卡和静态管理地址；默认管理地址为 `192.168.88.254/24`。ARM64 仅支持运行
Armbian Bookworm 的设备，解压一键安装包后以 root 执行 `./install.sh`，安装器保留
板卡内核、DTB、引导程序和 `/boot`。

初始 Web 账号为 `admin`，密码为 `password`，首次登录必须修改密码。升级包必须与
CPU 架构一致。升级前备份配置并保留可回退介质；ARM 一键安装包内附同版本使用说明。

## English

Every successful `main` build creates a GitHub Release named
`v0.1.<run_number>`. Tag and manual runs may provide a valid semantic version.
The release contains the x86-64 installer ISO and upgrade, the ARM64 Armbian
one-click installer and upgrade, plus one `SHA256SUMS` file.

Verify checksums before installation. The x86 installer interactively selects
the disk, management NIC and static address; the default is
`192.168.88.254/24`. The ARM64 installer targets Armbian Bookworm and preserves
the board kernel, DTB, bootloader and `/boot`. Sign in as `admin` / `password`
and change the password on first login. Never apply an upgrade for another CPU
architecture.
