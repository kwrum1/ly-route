# 运行态与容器性能验证

> 本文是功能批次和明确硬件任务的测试参考，不是日常开发门禁。热修复不需要等待容器全矩阵、性能或物理硬件；按[开发、热修复与验收流程](development-workflow.md)执行。

语言：[English](../runtime-hardware-validation.md) | 简体中文

本文档定义容器功能验收和性能回归。完整软件矩阵以[双产品全功能验收设计](container-network-validation.md)为准。仓库单元测试只能验证控制面和配置编译；Linux 容器网络必须验证 DNS/DHCP、路由/NAT、PPPoE、代理、QoS、服务链、故障恢复以及 PPPoE NAT/路由/64B 小包基准。真实硬件、驱动、温度和板卡行为不属于当前发布门禁。

## 容器功能验收（功能批次/发布前按需）

- 在 Linux CI/测试机使用独立 network namespace、veth、bridge 和 macvlan/等价二层接口；macvlan 依赖 Linux 宿主，不在 Windows Docker Engine 上执行。
- Gateway 拓扑至少包含：被测 rootfs/路由器、LAN 客户端、两个 WAN/互联网端、PPPoE 服务器、两个可区分的 DNS 上游、客户端私设外部 DNS、DHCP 测试端、代理固定节点与多个订阅候选、流量发生器和抓包点。
- DNS 必须证明 TCP/UDP 53 默认接管、固定 WAN/上游/代理、DNS 选线高于 PBR、TTL 内关联业务继承 DNS 线路、代理 DNS 请求本身入代理，以及未命中/线路失效返回 NODATA 且不回落。
- DHCP 必须证明多子网、池、选项、静态绑定、租约/在线用户、续租/释放、池耗尽、冲突和重启恢复；客户端手工改外部 DNS 后仍受 53 接管。
- PPPoE 服务器必须在容器中真实提供发现、认证、会话和地址分配；Gateway 容器通过隔离二层网络拨号，并验证断线、认证失败、重拨、路由安装和恢复。
- WAN 组分别验证主备、加权负载和五元组负载；明确拒绝代理逻辑 WAN 成员。
- 代理验证固定节点、滚动 ping 为主的最快节点、协议健康、失效自动切换、恢复和防抖。
- 两层 QoS 分别验证策略限速和内置智能 QoS；用双向大流把线路跑满，同时测量 ping、DNS、短连接、交互流量、吞吐、公平性和资源开销。
- Orchestrator 拓扑至少包含两侧流量端、被测容器、物理口/bond 逻辑端点及多个可观测服务节点，验证 `direct/drop/via`、严格逆序回程和节点失效默认 bypass。
- 每个场景保留拓扑、镜像/提交哈希、配置、pcap、计数器、API 状态、故障时间线和恢复结果。完整矩阵通过前不得宣布对应功能验收合格。

## 前置条件

- 将 Ly Route rootfs 或固件安装到目标设备。
- 确认包含 FD.io VPP、`ly-route-vpp-apply`、SmartDNS、xray、Kea DHCP、nginx 和控制面服务。
- 在 `/etc/ly-route/control-api.env` 配置本地管理员凭据。
- 仅在真实设备上启用运行态编排：

```sh
LY_ROUTE_ENABLE_SERVICE_RUNTIME=true
LY_ROUTE_SERVICE_ROOT=
```

- 至少连接一个 WAN、一个 LAN 客户端，以及可用时的真实 PPPoE 线路；真实 PPPoE 线路是运营商兼容性证据，不替代强制容器 PPPoE 功能验收。
- 只探测显式分配的数据接口，管理接口永久保留在 Linux。先探测并实测选择最优 VPP 原生高性能候选；原生候选全部失败后才探测 VPP DPDK。整台设备全部活动数据接口必须使用最高共同合格等级。两级都失败时数据面必须保持锁定，禁止 AF_XDP copy、generic XDP、AF_PACKET 或 Linux 转发降级。

## 控制面冒烟

1. 以管理员登录控制台。
2. 打开 `系统概况` 查看权威组件状态；apply 等动作仍位于 `系统维护`。
3. 点击 `刷新状态`，确认 SmartDNS、Kea、xray、PPPoE、VPP、nftables/TProxy、Linux routing 和 persistence 都有明确状态。
4. 点击 `预览运行态`，确认服务产物、VPP 操作、nftables/TProxy 计划和 Linux 策略路由计划可渲染且不泄露密钥。
5. 点击 `应用运行态`，结果必须是 `committed` 或带明确原因的 degraded/unavailable 状态。

等价 API：

```sh
curl -fsS http://127.0.0.1/api/v1/runtime/status
curl -fsS http://127.0.0.1/api/v1/runtime/preview
curl -fsS -X POST http://127.0.0.1/api/v1/runtime/apply \
  -H 'Content-Type: application/json' \
  --data '{}'
```

## 硬件检查项

- LAN 客户端通过 DHCP 获取 `192.168.88.100-192.168.88.199` 地址并可访问 `https://192.168.88.1/`。
- SmartDNS 监听 `192.168.88.1:53` 并返回预期解析结果。
- VPP 服务运行，`vppctl show version`、接口状态和转发路径可查询。
- PPPoE 预览和实际拨号不泄露密码，失败时返回明确 degraded 原因。
- xray 与 TProxy/nftables 规则按策略生成，订阅探测不得把订阅正文写入日志。
- 运行态 apply 后审计、快照和回滚路径可用。

## ARM 与 x86 调优

`/usr/lib/ly-route/tune-vpp.sh` 会根据 `LY_ROUTE_PLATFORM_ARCH` 或 `uname -m` 选择调优画像：

- x86：更激进的 hugepage、backlog、worker 和 `performance` CPU governor；4 核及以上为 Linux 管理面保留 CPU0，VPP 使用独立主核和 worker 核。
- ARM：限制 worker 和 hugepage 占用，使用更保守的 backlog、dirty ratio 与 `schedutil` governor。
- 结果写入 `/etc/ly-route/platform-tuning.env`，用于现场排查。

生产画像同时启用 IPv4/IPv6 转发，关闭会破坏多 WAN 非对称路径的反向路径过滤和 ICMP 重定向，并关闭 Linux RPS/XPS 的二次跨核分发。VPP API trace 默认关闭，仅在故障诊断期间临时启用。

Xray 的 HTTPS 订阅允许任意自签证书。TLS 节点无需用户指定 CA：系统 CA 验证失败时自动读取叶证书并生成 `pinnedPeerCertSha256`，兼容 Xray 26 且避免持续关闭身份校验；TLS 最低版本为 1.2。Reality 节点继续使用自身公钥机制校验。

## 安全变量

外部验证变量必须通过环境变量注入，例如：

```sh
export VCENTER_PASSWORD='...'
export LY_ROUTE_PROXY_SUBSCRIPTION_URL='...'
```

不要把这些值写入 shell history、文档、CI 日志或命令参数。
