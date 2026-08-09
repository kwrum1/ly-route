# ISO 打包流程冻结与全功能验收清单

> 状态：冻结基线，2026-08-09
>
> 适用范围：Ly Route 出口网关、Ly Route 编排器。两者源码、前端、后端和运行时服务必须继续保持产品隔离；本文只规定共同的交付与验收门禁。

## 1. 当前基线结论

ISO 打包流程已经冻结，但当前标准安装镜像仍需完成一次干净安装复测：

- 标准产物名称固定为：`ly-route-gateway-x86_64-installer.iso`。
- 此前记录的 SHA-256 和 `Installation complete` 来自旧 ISO 与复用的串口日志，不能作为当前产物证据。
- 上一份压缩磁盘载荷中的 VPP 运行库兼容 Bookworm，但未包含已验证的 VMXNET3 DMA 修复，不能继续作为发布基线。
- 编译机宿主直接生成的 VPP 包需要 `GLIBC_2.38`/`libc6 >= 2.42`，已判定为无效产物。当前必须在 Bookworm 隔离环境中重建并通过第 6 节门禁，才能注入标准 ISO。

安装通过和系统功能通过是两个独立门禁，不能互相替代。

## 2. ISO 打包流程（冻结，不得绕过）

### 2.1 唯一输入原则

1. 只使用当前完整源码树作为输入，禁止混用旧的 `/root/ly-route` 裁剪目录、旧 rootfs、旧 VPP deb 或旧 ISO。
2. 构建目标固定为 Debian Bookworm amd64。
3. VPP 必须由当前源码的 `scripts/build-vpp-bookworm-debs.sh` 生成，并且运行时包、开发包、插件包的版本必须完全一致：`25.10.0-release`。
4. `LY_ROUTE_EXTRA_DEBS_DIR` 中的 VPP 包必须与 rootfs 内的 `dpkg` 状态同时满足：
   - `vpp`、`libvppinfra`、`vpp-drivers`、`vpp-plugin-core` 的版本相同；
   - 依赖来自 Bookworm（`libc6 >= 2.34`），不得出现 `libc6 >= 2.38`、`libc6 >= 2.42` 或 `libssl3t64`；
   - `readelf --version-info` 对 `libvppinfra.so` 的最高 GLIBC 版本不超过 `GLIBC_2.34`。

### 2.2 固定构建顺序

以下顺序是唯一认可的发布顺序。每个阶段必须成功后才进入下一阶段：

```bash
set -euo pipefail
repo=/root/ly-route-worktrees/codex-handoff-20260730
cd "$repo"

export LY_ROUTE_MIRROR=https://mirrors.ustc.edu.cn/debian
export LY_ROUTE_SECURITY_MIRROR=https://mirrors.ustc.edu.cn/debian-security
export LY_ROUTE_EXTRA_DEBS_DIR=/root/ly-route/runtime-debs-gateway-bookworm-amd64

./scripts/build-rootfs.sh \
  --product gateway --arch amd64 --out dist/rootfs-release

rootfs=dist/rootfs-release/ly-route-rootfs-gateway-bookworm-amd64.tar.zst
./scripts/build-disk-image.sh \
  --product gateway --rootfs "$rootfs" \
  --out dist/x86-release --size 4G

image=dist/x86-release/ly-route-gateway-bookworm-amd64-4g.img.zst
./scripts/build-auto-install-iso.sh \
  --product gateway --image "$image" --out dist/iso-release
```

`build-auto-install-iso.sh` 每次构建前都会删除输出目录中的全部旧
`ly-route-gateway*.iso` 及其校验和/manifest。成功后只能留下一个标准
ISO 和三份配套文件。

### 2.3 每次产出必须执行的校验

```bash
sha256sum -c dist/x86-release/*.img.sha256
sha256sum -c dist/x86-release/*.img.zst.sha256
sha256sum -c dist/x86-release/*.manifest.json.sha256
sha256sum -c dist/iso-release/*.iso.sha256
file dist/iso-release/*.iso
xorriso -indev dist/iso-release/*.iso -report_el_torito plain
```

必须同时确认：

- ISO 是 ISO 9660、带 BIOS 和 UEFI 启动项；
- ISO 内载荷名称为 `ly-route-gateway-x86_64.img.zst`；
- ISO manifest 的嵌入载荷 SHA-256 与磁盘镜像压缩包一致；
- rootfs manifest、磁盘镜像 manifest、ISO manifest 的产品均为 `gateway`，架构均为 `amd64`；
- 构建日志出现 `Built ...rootfs...`、`Built ...img.zst` 和 `Built ...installer.iso`，没有仅凭文件存在判定成功。

### 2.4 ESXi 安装验收固定动作

1. 上传标准 ISO 并在 datastore 校验 SHA-256，然后删除根目录中其他全部 `ly-route-gateway*.iso`；不得删除无关 ISO 和虚拟机目录。
2. 关闭旧验收 VM，确认只保留明确的系统盘和四个测试网卡。
3. 挂载本轮 ISO，确认 CD-ROM 路径只出现一次 datastore 前缀。
4. 为本轮创建独立 serial log，禁止复用旧日志判定安装成功。
5. 必须看到：选择/确认安装目标、写入进度、`Installation complete`、重启倒计时。
6. 弹出 ISO 后从系统盘启动，再开始功能验收。
7. 如果安装日志没有上述证据，不能进入 VPP、PPPoE、DNS 等功能测试。

### 2.5 冻结规则

- 不得为了修复功能临时修改 `build-rootfs.sh`、`build-disk-image.sh`、`build-auto-install-iso.sh`、ISO 内安装脚本或载荷布局后继续沿用旧基线。
- 任何打包脚本、VPP 包版本、rootfs 包清单、安装脚本的修改，都必须重新执行 2.3 和 2.4；新产物校验通过后立即清理旧 ISO，禁止在验收环境保留多个易混淆版本。
- 功能修复优先在源码、运行时 deb 和容器/VM 中验证；需要重新生成 ISO 时只能按本文顺序生成，禁止手动拼装或复制旧文件。
- 版本冻结期间，网关和编排器不得共用产品包、前端目录或服务清单。

## 3. 已知问题登记

状态含义：`通过` = 有直接运行证据；`待修复` = 已有失败证据；`待验收` = 尚未完成真实 UI→API→数据面链路。

| 编号 | 产品 | 问题 | 直接证据 | 状态 |
|---|---|---|---|---|
| ISO-001 | 网关 | 标准 ISO 必须完成启动、写盘和重启；此前串口日志属于旧安装，不能作为本轮证据 | 待使用独立新串口日志复测 | 待验收 |
| ISO-002 | 网关 | ESXi 视频与串口启动已经验证，等待交互安装器重建后复测 | 已看到本轮串口启动日志 | 待验收 |
| ISO-003 | 网关 | 安装器已进入磁盘选择，但 unit 使用 `StandardInput=null` 导致无法读取 `/dev/tty` | `ttyS0` 绑定与读取回退已修正，等待重建 | 待修复 |
| PKG-001 | 网关 | 磁盘载荷已确认兼容 Bookworm，但验收 VM 仍在启动旧系统 | 载荷最高需要 `GLIBC_2.34`；旧系统需要 `GLIBC_2.38`/`libc6 >= 2.42` | 待验收 |
| VPP-001 | 网关 | VPP 服务、`vppctl`、数据接口未通过 | VM 当前只能得到 `vppctl` 动态库错误，不能以 `local0` 判定通过 | 待修复 |
| NIC-001 | 网关 | 管理口与数据口所有权、VMXNET3 原生路径未完成实机证据闭环 | 尚未同时证明 Linux 管理口、VPP 数据口、PCI 驱动归属和收发包 | 待验收 |
| DNS-001 | 网关 | SmartDNS、DNS 透明劫持、VPP DNS 代理启动链未验收 | 旧系统曾出现 DNS 单元级联失败；新 rootfs 尚未完成运行态证据 | 待验收 |
| GW-001 | 网关 | PPPoE 客户端拨号并获得 `10.1.18.0/24` 外网地址 | 尚未在干净系统上由真实 UI 配置后完成拨号和上网 | 待验收 |
| GW-002 | 网关 | LAN DHCP、NAT、端口映射、IPv6 PD/RA | 尚未完成真实客户端闭环 | 待验收 |
| GW-003 | 网关 | 国内直连/国外代理的流量分流与 DNS 分流 | 尚未完成 geoip/geosite、国内 Bootstrap、国外 Bootstrap 的真实解析和连接证据 | 待验收 |
| GW-004 | 网关 | QoS、安全控制、IP 组/范围/掩码和 any 语义 | 尚未完成真实 UI→API→VPP 数据面闭环 | 待验收 |
| GW-005 | 网关 | 系统概况、在线用户、Top 连接/域名、WAN 图表、配置和升级 | UI 已冻结方向，运行态数据和升级回归尚未完成 | 待验收 |
| ORCH-001 | 编排器 | 编排器必须与网关隔离，不承担 NAT、DHCP、DNS | 产品边界已定义，独立产物和 VM 尚未完成 | 待验收 |
| ORCH-002 | 编排器 | 中文编排组、拖动组优先级、策略明细修改/删除 | UI 形态已完成部分验收，API→VPP 流量路径未闭环 | 待验收 |
| ORCH-003 | 编排器 | 多虚拟客户端的 ACL、any、端口/协议、IP 组和限速 | 尚未完成并发真实流量验证 | 待验收 |

## 4. 全功能验收标准

### 4.1 网关（先验收）

- [ ] 干净 VM 能从标准 ISO 安装到系统盘，并产生本轮独立 serial log 证据。
- [ ] 安装后 VPP 包与 rootfs 包版本、依赖、GLIBC 需求完全一致。
- [ ] 管理接口得到静态管理地址，浏览器能登录；管理口不被 VPP 独占。
- [ ] 剩余网卡按 MAC/PCI 映射进入 VPP；可读出原生高性能路径，不能用时按规则回退，全部不可用时锁定并报告原因。
- [ ] 由 UI 配置 PPPoE 账号密码，VPP PPPoE 客户端拨号成功，WAN 获取真实地址并能访问 `10.1.18.0/24` 外部网络。
- [ ] UI 配置 LAN 地址（192/172 网段）、DHCP，虚拟真实客户端能获得地址、网关和 DNS。
- [ ] 出站 NAT 的 `any` 只保护出站行为；端口映射/ACL 等入站或双向服务保留双向语义。
- [ ] 端口映射由外部客户端访问时能到达 LAN 目标，删除/修改后数据面同步失效/生效。
- [ ] 所有终端的 53 流量透明劫持；终端手填 `223.5.5.5` 等 DNS 仍按 DNS 策略分流。
- [ ] DNS 策略中 geosite 命中使用国内 Bootstrap；未命中 geosite 使用国外 Bootstrap；上游 DoH 仍由用户配置。
- [ ] 国内 geosite/geoip 走 PPPoE，国外缺省走代理 WAN；代理订阅/节点、固定节点、延迟最快节点和故障切换真实有效。
- [ ] 基础限速与内置不可修改的智能 QoS 分别验证；安全策略、IP 单值/IP 段/CIDR/IP 组/any 均能由 UI 配置并进入数据面。
- [ ] IPv6 PD/RA 从指定 WAN 获取前缀后下发到 LAN，客户端获得 IPv6 并可路由访问。
- [ ] 系统概况、WAN 上下行总和趋势、在线 IPv4 数、Top 连接/域名和出口图表显示真实接口数据。
- [ ] 配置备份/恢复、管理设置、升级包上传、升级后重启和回滚边界完成验证。

### 4.2 编排器（网关通过后独立验收）

- [ ] 编排器使用独立前后端、独立镜像和独立服务清单；不提供 NAT、DHCP、DNS 管控假功能。
- [ ] 只能选择完全无 LAN/WAN 属性的两个接口建立编排组，方向为进线/出线，名称支持中文。
- [ ] 编排组可折叠、拖动组标题条改变顺序；策略明细支持新增、编辑、删除，流量路径显示编排组名称。
- [ ] 策略匹配支持 any、单 IP、IP 段、CIDR、IP 组、协议和端口；组顺序和明细序号产生确定优先级。
- [ ] 多个虚拟客户端同时通过不同编排组，ACL、双向边界和限速规则均以真实报文验证。
- [ ] 系统概况、编排组运行状态、路径流量和连接状态与 API 返回一致，空数据也保持固定行高、分页和布局。

## 5. 每轮回归的最短流程

每轮必须先清除旧环境，避免历史状态污染：

1. 保存上一轮日志和 manifest，关闭并清理旧验收 VM/系统盘；PPPoE 服务器和客户端网络命名空间也重建。
2. 按第 2 节生成唯一新产物，记录 SHA-256。
3. 从 UI 完成配置；同时记录浏览器请求、API 最终配置和服务日志。
4. 从虚拟客户端发真实 DHCP、DNS、TCP/UDP、端口映射和代理连接；只以报文和数据面状态作为功能证据。
5. 失败项登记编号，修复后只重建受影响的 runtime/rootfs/ISO，仍必须走同一冻结流程。
6. 只有“UI 配置成功 + API 收到最终配置 + 数据面生效 + 清洁回归成功”四项同时成立，才能在表格中勾选通过。

## 6. “启动但未接管”问题的固定修复流程

本节是强制门禁，后续不得跳过。之前反复失败的直接原因是两个问题叠加：

1. VMXNET3 数据口被错误地留给 Linux，VPP 的 VMXNET3 PCI 驱动因此报
   `device not bound to vfio-pci or uio_pci_generic`，只剩 `local0`；
2. 仅手工绑定 VFIO 不能证明 DMA 可用。未包含 `vlib_pci_map_dma` 的 VPP
   插件在该虚拟 PCI 环境中会产生 DMAR fault，因此必须使用带 VMXNET3 VFIO
   DMA 映射补丁的 VPP 包。

### 6.1 修复顺序（只允许这一条路径）

1. **先复现**：收集 `systemctl status`、`journalctl -b`、`vppctl show interface`、
   PCI 驱动归属和内核 DMA 日志，区分服务启动、设备所有权和 VPP DMA 初始化问题。
2. **本机挂载热修复**：只挂载 canonical `.img` 根分区，注入脚本或目标 Bookworm
   包；禁止把编译机宿主系统直接编译的 VPP deb 注入 Bookworm 镜像。
3. **包门禁**：对每个 deb 执行 `dpkg-deb -f` 依赖检查，确认架构、版本、libc/SSL
   依赖属于 Bookworm，并确认 `vpp-plugin-core` 来自带
   `0001-vmxnet3-map-vfio-dma.patch` 的源码构建。
4. **热修复验证**：启动镜像并确认 `ly-route-vfio-preflight`、`vpp` 为 active；
   数据 PCI 驱动归属为 `vfio-pci`，VPP 创建数据接口成功，内核日志不得持续出现
   `DMAR`、`PTE Write access is not set` 或 `device not bound`。
5. **再注入 ISO**：只有第 4 步通过，才卸载旧介质、加载新 canonical ISO，看到磁盘和
   网卡选择、写盘进度、`Installation complete` 与重启倒计时，然后弹出 ISO 从系统盘启动。
6. **最终登记**：每次只保留 `ly-route-gateway-x86_64-installer.iso` 及校验/清单，
   每轮使用新的 serial log；证据不完整只能记为“待验证”。

### 6.2 路径选择边界

- 管理口始终由 Linux 保留，不交给 VPP。
- 物理数据网卡按能力探测选择最高等级的 VPP 原生路径，不能用时才回退 DPDK。
- ESXi VMXNET3 必须走 VPP VMXNET3 驱动并完成 VFIO/UIO PCI 所有权交接；Linux 仍持有
  `vmxnet3` 不能判定为 VPP 原生已接管。
- AF_XDP/AF_PACKET 只作为容器或实验环境路径，不替代生产数据面结论。
- 生产包必须在 Debian Bookworm 目标环境中生成；宿主机生成的 `libc6 >= 2.38`、
  `libssl3t64` 等包一律拒绝进入镜像。

### 6.3 当前门禁

- [ ] Bookworm VPP 包构建完成，且 `vpp-plugin-core` 包含 VMXNET3 DMA 修复。
- [ ] 本机挂载镜像热修复后，VPP 接管数据口且无 DMA fault。
- [ ] 从已验证镜像注入的 ISO 完成一次清洁安装并从系统盘启动。
- [ ] 安装后的管理口可访问 UI，数据口由 VPP 接管，并有本轮新 serial log 证据。
