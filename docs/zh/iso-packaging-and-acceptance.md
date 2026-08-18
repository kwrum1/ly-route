# ISO 打包与安装验收

> 当前有效范围：发布构建和安装冒烟。热修复不需要执行本文；日常流程见[开发、热修复与验收流程](development-workflow.md)。

## 产物

x86 网关的唯一安装产物：

`ly-route-gateway-x86_64-installer.iso`

同时生成 ISO 校验和、manifest、磁盘镜像和升级包。每次构建使用独立输出目录，构建脚本负责清理同目录的旧 Ly Route 产物，不能用旧 ISO 或旧串口日志代替当前产物。

出厂管理地址为 `192.168.88.254/24`，网关为 `192.168.88.1`。安装器枚举磁盘和网卡，按 MAC/PCI 保存管理口与数据口映射；管理口保留 Linux 所有权，数据口由运行时能力选择器处理。

## 固定发布流程

```bash
set -euo pipefail
repo=/root/ly-route
cd "$repo"

./scripts/build-rootfs.sh --product gateway --arch amd64 --out dist/rootfs
rootfs=dist/rootfs/ly-route-rootfs-gateway-bookworm-amd64.tar.zst
./scripts/build-disk-image.sh --product gateway --rootfs "$rootfs" \
  --out dist/x86 --size 4G
image=dist/x86/ly-route-gateway-bookworm-amd64-4g.img.zst
./scripts/build-auto-install-iso.sh --product gateway --image "$image" \
  --out dist/iso
```

运行包、插件和 rootfs 必须来自同一次构建，架构、VPP 版本和 Bookworm 依赖必须一致。网关需要的插件清单以 `docs/zh/iso-plugin-packaging-boundary.md` 和构建 manifest 为准；文件存在不等于运行态通过。

## 发布前最小检查

```bash
./scripts/ci-release-verify.sh
sha256sum -c dist/iso/*.iso.sha256
file dist/iso/*.iso
xorriso -indev dist/iso/*.iso -report_el_torito plain
```

发布构建只需要确认：源码可编译、产品包边界正确、rootfs scaffolding 正确、ISO 可启动且副产物校验通过。完整功能在发布前的功能批次中完成，不在这里重复逐项测试。

## 安装冒烟

1. 使用本轮唯一 ISO，创建独立串口或控制台日志。
2. 确认安装器能选择磁盘和管理口、写入载荷、校验写入结果并重启。
3. 从系统盘启动后确认管理地址、UI/API、VPP/相关服务和已保存的 MAC/PCI 映射。
4. 只做一次基础启动冒烟：管理面可访问，受影响的数据面服务可查询。

ESXi、VMXNET3、AF_PACKET、VFIO、IOMMU、物理 PCI 所有权、性能和温度不属于 ISO 安装冒烟；需要时另开硬件验证任务。它们不能阻塞日常热修复，也不能把虚拟机结果当成物理性能证据。

## 修复与重新打包

- 功能 Bug：先按热修复流程在现有环境替换受影响文件，原场景通过后再决定是否进入功能批次。
- 只有 rootfs、插件清单、安装器或发布脚本变化，才必须重建 ISO。
- 重建时保留上一轮日志到归档目录，清理输出目录旧 ISO、校验和和 manifest；不删除无关文件。
- 新 ISO 的名称、SHA-256、源码提交和构建时间写入同一 manifest。
