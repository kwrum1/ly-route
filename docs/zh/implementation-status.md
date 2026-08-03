# LY-Route 当前实现盘点

更新日期：2026-08-02
接盘分支：`codex/handoff-20260730`
代码基线：`cfbd694` 加当前集成工作区未提交修改

## 1. 当前结论

### 当前接盘进度（2026-08-02）

- 后端源码已按产品物理隔离：`backend/gateway`、`backend/orchestrator`，启动入口分别为 `backend/cmd/gateway-control` 和 `backend/cmd/orchestrator-control`。共享底层库仍位于 `backend/internal`，只承载通用 HTTP、持久化、VPP 和运行时能力。
- 前端源码已按产品物理隔离：`frontend/gateway`、`frontend/orchestrator`；旧的 `frontend/controller-shell` 和 `backend/cmd/ly-route-control` 已移除。两个前端共用浅蓝白设计基线，但菜单、内容和操作边界分别服务出口网关与流量编排器。
- 后端门禁已通过：Go 全量测试、网关产品契约、编排器容器 API/持久化/重启验证、双产品 bundle 隔离、rootfs scaffold 验证均通过。编排器容器证据位于 `.sisyphus/full-acceptance/evidence/o-runtime-container/`。
- 两套前端已经完成源码、构建和产品边界隔离，并具备真实后端 HTTPS 容器浏览器基线；但当前页面布局、交互和用户文案未达到产品要求，不能再称为“UI 重构完成”。现有 UI 仅保留为 API/字段盘点和回归参照。
- 新增 `deploy/orchestrator-demo/` 和 `scripts/build-orchestrator-demo-image.sh`，Orchestrator 容器直接启动 `cmd/orchestrator-control`，不使用 UI fixture。该演示容器没有物理 VPP 拓扑，因此运行态明确显示 VPP degraded/locked；这用于验证真实 API 的诚实降级，不代表数据面已经伪造为运行中。
- 现有浏览器证据位于 `.sisyphus/full-acceptance/evidence/g-ui-live/` 和 `.sisyphus/full-acceptance/evidence/o-ui-live/`，只能证明基线页面可打开和产品边界基本成立，不能证明视觉、交互或产品成熟度验收通过。
- 当前 UI 计划采用严格页面验收制：从 Gateway 与 Orchestrator 登录页开始，先做现状 QA，产品经理提出什么就定向修改什么；重建两个完整容器并明确验收后才进入下一页面。当前未获确认的全局框架改动已撤回，应用内页面暂不修改。全部显示与交互页面验收后，再盘点 VPP、后端和 API 能力并由产品经理批准功能拓展。

项目已经形成两个构建时固定、不能运行时互转的产品：

- `gateway`：出口网关，负责 LAN/WAN、WAN 组、策略路由、NAT、DNS、DHCP、PPPoE、代理出口、安全、两层 QoS、遥测和系统维护。
- `orchestrator`：透明流量编排器，只负责 WAN 到 LAN 之间的流量编排、流控、安全和 IP 对象，不承担 NAT、DNS、DHCP、PPPoE、WAN 组、端口映射、代理和域名功能。

当前统一容器/浏览器子集 `33/33` 通过，全发布矩阵 `39/45` 通过（`86.7%`）。新增的 PPPoE 容器性能门禁已实际测量 NAT44、纯路由和 64B 小包；安全故障的真实 VPP 规则保留与运行时 API 审计也已合并取证。当前仍有 Gateway 遥测、运维运行/故障、全页面 UI、Gateway 制品，以及 Orchestrator 制品共 6 项未完成。因此当前是“功能集成候选”，不是“生产验收合格”。

## 2. 双产品共享能力

| 能力 | 当前状态 | 结论 |
| --- | --- | --- |
| 产品隔离 | 已实现 | API、资源、数据库、前端 bundle、rootfs 服务和升级校验均按产品隔离。 |
| 登录、用户、权限、审计 | 已实现 | 支持管理员/只读角色、会话、密码修改和敏感信息脱敏。 |
| 管理口 | 容器闭环 | 支持 `exclusive` 和 `shared_lan`。共享模式可在 LAN 物理口上配置管理 IP/掩码/网关，例如 `eth2` 同时承载 LAN 和 `10.10.10.254/24` 管理地址；本机管理流量经 stock VPP LCP 进入 Linux，普通转发仍走 VPP。重启和防失联回滚已纳入容器故障验证。 |
| 最高共同合格等级 | 代码闭环 | 对所有活动数据口统一探测和选择；优先已证明的 VPP 原生 AF_XDP 零拷贝/RDMA-DV，原生均不合格时才事务化切换 DPDK/VFIO。原生附件已改用 stock VPP 25.10 `create interface af_xdp ... zero-copy` / `create interface rdma ... mode dv`，并以硬件接口和宿主 netdev 双重回读；容器 veth 不支持零拷贝时会锁定且不遗留接口。不能按端口混用等级。 |
| 禁止慢路径降级 | 已实现 | Linux forwarding、AF_PACKET、generic XDP、nftables TProxy 和 Linux policy routing 均不作为生产回退；无合格高性能路径时保持管理可达并锁定转发。 |
| DPDK 回退 | 代码与制品闭环 | 包、能力探测、VFIO 绑定、启动、回读、重启、失败回滚和残留清理已实现；仍缺目标网卡性能与 IOMMU 故障矩阵。 |
| 配置事务 | 部分闭环 | 已有 preview/apply、语义回读、receipt、回滚和启动恢复基础；仍需全功能组合故障与断电验证。 |
| x86 制品 | 部分闭环 | 双产品构建、依赖隔离、静态 rootfs smoke 已通过；真实启动、升级回滚和签名发布未完成。 |
| ARM64 制品 | 未完成 | Smart QoS 插件和完整双产品 ARM64 构建/升级矩阵尚未闭环。 |

## 3. 出口网关盘点

| 功能 | 状态 | 已完成 | 未完成/发布门禁 |
| --- | --- | --- | --- |
| LAN/WAN 与聚合 | 生产事务与容器闭环 | 物理口、active-backup bond、静态/DHCP IPv4/IPv6；生产多资源事务直接驱动 stock VPP 并语义回读；LAN↔WAN 双向包流与接口计数通过；第二个新 Bond 成员应用失败时会清理部分 generation 并恢复原接口/Bond；共享 LAN 管理 HTTP 在事务回滚和 WAN admin down 期间保持可达；可信完整读回后的已知 admin-state 漂移可自动修复，读回不完整仍锁定 | 目标硬件 driver/carrier 自动恢复、热插拔和更多 Bond 模式 |
| WAN 组 | 容器闭环 | 主备、加权负载、五元组负载；拒绝代理成员；故障与恢复包流通过 | 长稳和物理链路切换 |
| 策略路由 | VPP 包流闭环 | 有序 ACL/ABF/FIB、`any`、单 IP、CIDR、范围、IP 组、应用/回读/删除 | 更多组合优先级和全页面统一控件 |
| NAT/端口映射 | NAT44 容器闭环 | NAT44 包流、静态映射和端口映射生命周期 | 公网实线、更多故障和 IPv6 NAT 边界 |
| DNS 透明劫持 | 契约与容器闭环 | LAN 终端即使手工设置 `223.5.5.5` 等任意 IPv4/IPv6 DNS，TCP/UDP 53 仍由 VPP ACL/ABF 接管；保留终端身份和原目标响应源地址；DNS 固定出口优先于普通策略路由；直连解析必须且只能显式选择上游或 WAN；代理 DNS 随代理出口；未命中、上游失效或全部策略禁用均返回 NODATA，不泄漏、不降级到低优先级规则；API 拒绝隐式上游、通用路由/NAT 和 DoH/DPI 伪能力且保持零写入并审计 | 服务重启漂移和实机恢复 |
| DHCP | 服务与容器闭环 | Kea 地址池、静态绑定、真实客户端获取；生产 memfile 租约采集按最后记录过滤释放/过期项，API/在线用户真实读回；Kea 重启保持租约，租约库丢失明确失败且不返回陈旧数据或 client-id/user-context | 冲突压力、大租约量、断电续租和目标硬件 |
| PPPoE | 服务、包流与性能容器闭环 | PPPoE 服务器容器、PAP/CHAP、IPCP/IPv6CP、断线重拨与 VPP 会话读回已通过；自研 VPP PPPoE 客户端的重复应用已改为幂等，IPv6CP 协商地址会写回会话。DHCPv6-PD、LAN 前缀下发、RA 和终端全局 IPv6 尚未宣告通过：当前容器的 AF_PACKET 虚拟 WAN 夹具未把 DHCPv6 包稳定送达服务器，需在不改变生产路径的前提下补齐验收夹具。额外的 x86_64 容器基准在真实 PPPoE 会话上测量 NAT44、纯路由及 64B 小包，并保存损失率和 VPP 计数器。 |
| 代理出口 | 服务与容器闭环 | 单节点/订阅、固定节点、ICMP ping 初始主排序、Xray leastPing 自动切换、失败关闭和恢复、underlay 约束；loopback-only RoutingService 真实读回 balancer 当前节点，API 在全节点失效时降级而不伪报 `live_verified` | 代理 DNS 选线、长稳、防抖参数和硬件性能；明确不支持代理群组 |
| 用户限速 | VPP 包流闭环 | VPP policer 分类、限速、读回和真实速率验证 | 完整异常/重启矩阵 |
| 内置 Smart QoS | 生产插件与包流闭环 | 自研 VPP 插件已打包；五元组队列、主机/流 DRR、CoDel、整形、多 worker 单调度器；双向 2 Mbps 测试约 1.919 Mbps，主机聚合公平，满载 ping 最大约 33.5/38.5 ms | 故障回滚、长稳、重启、目标速率/多核/硬件矩阵 |
| 安全控制 | VPP 包流与运行时 API 部分闭环 | 严格 L2-L4 契约和 VPP ACL 拒绝/恢复包流通过；聚合安全代际已真实证明 IPv4 合法 IP-MAC 放行、MAC 欺骗阻断、IPv4/IPv6 威胁 IP 阻断和删除后恢复。自研 `ly_route_security_guard` 插件已接入配置代际、读回、删除与 Gateway deb，真实报文验证 SYN、UDP、ICMP、ICMPv6 阈值强制丢弃，以及 SYN 告警计数且不丢包；Gateway 的只读 `/api/v1/security/runtime` 仅在严格 VPP 读回成功时返回规则命中、超额、告警、丢弃计数，失败时明确 locked/degraded；不会以 Linux/nftables 慢路径代替 | 命中原因细分、告警审计、失败回滚和完整专属 UI；不包含 DPI/应用识别/上网行为审计 |
| 对象管理 | 部分闭环 | IP/域名对象、引用保护、CIDR、连续范围；路由支持 IP 组 | 其他页面统一成熟控件 |
| 遥测与维护 | 部分闭环 | 系统/接口/出口/连接/用户/域名/策略统计，配置导入导出、快照、恢复和固件 API | 长稳准确性、完整灾难恢复和升级回滚 |
| UI | 首轮重构闭环 | 统一浅蓝白视觉、Gateway/Orchestrator 独立 bundle、产品化菜单与真实后端容器浏览器验收；Gateway 19 个入口、Orchestrator 12 个入口分别验证桌面/移动端 | 按真实 VPP/PPPoE 容器数据继续补充全页面数据状态、错误态、配置组合和视觉细节 |

Smart QoS 不再依赖 DPDK HQoS。它是独立的生产级 VPP 插件，能运行在合格的 VPP 原生路径或 DPDK 回退路径上。网卡路径选择与 QoS 算法资格是两个独立门禁。LAN 必须显式填写下载带宽，WAN 必须显式填写上传带宽；系统不会猜测网卡标称速率。

## 4. 流量编排器盘点

产品拓扑遵循最新边界：一个唯一逻辑 WAN、一个唯一逻辑 LAN；二者可使用一个物理口或一个聚合组。只有 LAN/WAN 均存在后才能创建编排组。每个编排组严格使用两个未占用物理口，一个接 LAN、一个接 WAN，可自定义名称，不允许聚合口。

本轮已关闭此前复核出的三个主链路 P0 缺口：拓扑保存会自动建立 WAN 入、LAN 出的 VPP 原生透明数据面；整套策略保存会展开 `any`、IPv4/IPv6、CIDR、连续范围和 IP 组并原子提交；编排组只需要两个方向端口和名称，不需要服务节点 L3 地址或隐藏下一跳。生产透明运行时不再注册旧的逐五元组 service-chain 接口，也不再启动旧 intent reconciler。

真实 VPP 双 worker 容器已验证：默认直达、单组和双组顺序 `via`、`drop`、IPv4、IPv6、VLAN 100 标签保留、正反向对称、链路 down 默认 bypass、链路恢复重新入链、策略命中、Top 连接、产品 API 自动创建 LAN/WAN bond、成员故障切换、控制进程重启复用 bond 并自动重放、删除拓扑清理 bond 后进入 `locked`，以及从正式 deb 解包加载插件。系统概况现以实时 VPP generation 回读生成 apply/readback 收据，配置漂移会显示 degraded，重新提交后恢复 running。VPP 与数据库之间采用 `0600` 原子意图日志；模拟数据库提交后日志未清理的崩溃，启动会按持久化拓扑对账 bond 和插件，语义读回成功后才清除日志。该结果代表透明编排主链路达到生产候选，不代表应用层节点心跳、ARM64、目标硬件和全发布矩阵已经验收。

| 功能 | 状态 | 已完成 | 未完成/发布门禁 |
| --- | --- | --- | --- |
| 产品边界 | 已实现 | 排除 NAT、DNS、DHCP、PPPoE、WAN 组、端口映射、域名、代理、Top 域名 | 继续做越权 API/制品检查 |
| 网卡与逻辑 LAN/WAN | 物理口与 bond 容器闭环 | 唯一性、物理口/bond、成员占用和共享管理 LAN 约束；产品 API 自动完成 VPP bond 创建/差异变更/语义回读/失败回滚/重启复用/删除；透明插件在成员口捕获并归一化到逻辑 bond；成员故障与恢复包流通过；原子意图日志覆盖 VPP/数据库崩溃窗口并在启动时恢复 | 真实断电、更多 bond 模式/热变更压力、物理 NIC |
| 编排组 | 容器闭环 | 两物理口、方向唯一、命名、占用保护、API/持久化/UI | 多组并发、热变更和硬件 |
| 流量编排 | 生产候选容器闭环 | VPP 原生透明插件；拓扑/整套策略事务下发与语义回读；零隐藏下一跳；默认直达；`via/direct/drop`；IPv4/IPv6、VLAN、宽匹配和 IP 对象展开；双编排组按策略组顺序串联并严格反向返回 | 更多端口协议组合、IPv6 扩展头/分片、长稳/压力矩阵 |
| 节点故障 | 链路级容器闭环 | 任一组臂 admin/carrier down 时按包跳过该组，默认 bypass；恢复后重新入链；VPP 原生 bypass 计数进入 API/UI | 无节点管理地址条件下的可选二层心跳、连续失败/恢复阈值、抖动、应用进程假活和多节点矩阵 |
| 流量概况/系统概况/在线用户/Top连接 | 容器运行门禁闭环 | VPP 编排组双向字节/速率、邻居在线用户、趋势、原生规则计数、五元组 Top 连接、5 分钟老化、多 worker 聚合和 bypass 状态；系统概况实时校验 generation、收据、新鲜度及漂移 degraded/recovery；不读取 DHCP/NAT/DNS | 长稳、流表容量/碰撞压力、丢弃原因细分和目标硬件开销 |
| 流量控制 | 容器部分闭环 | 中性 `/api/v1/flow-control/policies`、类型化限速规则、runtime apply/API 回读；同一 VPP 路径上的服务链与 policer 包流、符合/超限计数已验证；编排器不能访问 Gateway 旧别名和内置 Smart QoS 状态 | 失败回滚、重启和更广组合矩阵 |
| 安全控制/IP 管理 | UI/API 闭环 | ACL 与 IP 组 CRUD、保存后回读和三视口浏览器流程通过；IP 组支持单 IP、CIDR、连续范围，编排器 API/配置导入/快照均拒绝域名组 | IP 组引用编排策略的完整 UI、ACL 组合包流和故障恢复 |
| 系统用户管理 | UI/API 闭环 | 菜单、管理员/只读用户新增修改删除、API 回读和三视口浏览器流程通过 | 会话并发、密码策略和故障矩阵 |
| 配置管理 | UI/API 闭环 | 产品隔离导出、两阶段导入预检/确认、快照创建和恢复均有三视口浏览器流程 | 整机恢复、升级和断电 |
| UI | 真实后端容器首轮闭环 | 独立 Orchestrator bundle、12 项产品菜单、流量概况真实 API、策略/对象/流控/安全/用户/配置页面、桌面/移动浏览器验收；演示容器中的 VPP locked 状态按真实能力明确展示 | 文案统一、事务/漂移详情、四视口视觉基线、错误态和完整组合数据面验收 |
| VPP 插件制品 | amd64 deb 闭环 | `ly-route-vpp-orchestrator` 构建脚本、deb、Orchestrator rootfs 必需包、运行时插件/CLI 门禁和 CI 内容检查；正式 deb 插件通过真实包流 | ARM64 插件包、安装/升级/回滚和签名发布 |

## 5. 验收事实

2026-08-01 的统一运行器通过 33 项：VPP VCL、原生附件失败锁定、DNS 源选路、DNS 适配器包流、任意目标 DNS、共享 LAN 管理、Gateway 网络生产事务/双向包流/故障恢复、SmartDNS TTL、Kea 租约/API/重启/故障、PPPoE 协议/API 生命周期/故障恢复、NAT44、安全 ACL、安全代际 IP-MAC/威胁名单包流与删除恢复、DNS 优先级、WAN 组、用户限速、Smart QoS、Xray 正式二进制配置、代理最快节点选择/真实状态读回/故障切换/失败关闭/恢复、Gateway 产品/负面/服务/安全/DNS 正向/DNS 负向契约、Orchestrator 产品/策略/负面/共同数据面契约、透明编排产品/API/bond/遥测包流、bond 安全与流控、Gateway 浏览器、Orchestrator 浏览器。额外的 VPP 安全插件专项证据覆盖 SYN、UDP、ICMP、ICMPv6、告警、禁用和删除；透明编排用例只通过产品 API 提交拓扑和整套策略，不再调用旧逐流 service-chain apply；其规范化证据目录为 `.sisyphus/full-acceptance/evidence/o-transparent/`。

机器可读发布矩阵为 `39/45`，其中 Gateway `29/34`、Orchestrator `10/11`。尚未通过的场景不能用单元测试或页面存在替代，当前集中于 Gateway 遥测、运维及其故障矩阵、全页面 UI、Gateway 镜像启动升级回滚，以及 Orchestrator 镜像启动升级回滚。真实硬件实验已从当前发布门禁移除，性能基准改由容器化 PPPoE 环境持续执行。

当前计划只以 [工作计划](../work-plan.md) 和 [中文详细计划](work-plan.md) 为准；本文件只陈述已验证事实。注意：`go test ./...` 必须在 `backend/` 模块目录执行；本轮正确目录回归通过。
