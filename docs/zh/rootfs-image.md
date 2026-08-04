# 网关固件产物

生产工作流为 `.github/workflows/gateway-release.yml`，当前只发布出口网关，编排器打包保持暂停。

## x86-64

- 4 GiB GPT IMG，支持 BIOS 和可移动 UEFI GRUB 启动；
- Web 控制台使用的 `amd64` 升级包。

## ARM64 / Armbian

ARM 任务运行在 GitHub 原生 ARM64 runner，并从固定提交构建 VPP，输出：

- Armbian Bookworm ARM64 一键安装包；
- Web 控制台使用的 `arm64` 升级包。

一键安装是在现有 Armbian 上叠加 Ly Route，保留板卡 kernel、DTB、bootloader、分区布局和 `/boot`。安装阶段不把网卡预先绑定到 VFIO；管理员配置 LAN/WAN 后才执行数据面能力探测与选择。

## 默认访问

- 首个以太网口管理回退地址：`192.168.88.1/24`；
- 控制台：`https://192.168.88.1/`；
- 账号 `admin`，密码 `password`，首次登录强制改密；
- 未完成 LAN/WAN 分配和数据面能力证明前，转发保持锁定。

完整构建命令、ARM 支持范围和产物名称见仓库根目录 [`README.md`](../../README.md)。
