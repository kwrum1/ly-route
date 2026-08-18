# Ly Route 研究记录

语言：简体中文

本文档记录研究决策，不是日常开发门禁。标记为生产基线的项目属于产品范围；标记为移除或仅研究的项目不得在没有明确需求时进入实现。开发阶段和门禁以[开发、热修复与验收流程](development-workflow.md)为准。

## 代理与透明交接候选项

| 项目 | 当前状态 | 处理建议 |
| --- | --- | --- |
| Xray virtual-WAN proxy gateway | 生产基线 | 保留并实现，但业务流从 VPP 进入受控服务接口，不得以 Linux TProxy 作为生产转发路径 |
| proxy_egress 逻辑 WAN 行渲染 | 生产基线 | 保留并实现 |
| SmartDNS 到代理联动 | 生产基线 | 保留并实现 |
| VPP steering 到代理 underlay | 生产基线 | 保留并实现 |
| nftables / TProxy / Linux policy routing 透明劫持 | 禁止作为生产数据面 | 只能用于主机防护、安装期诊断或明确隔离的遗留测试；不得接管 Gateway 或 Orchestrator 业务报文 |
| VPP 到代理的受控服务接口 | 产品必需，尚未实现 | VPP 必须保有业务报文的分类、选路和读回；服务接口需保留原始目的地址、双向会话语义、回注和故障失败关闭证据 |
| memif -> zmemif stateful flow translator | 研究候选 | 可作为受控服务接口的实现候选，使用前必须具备双向会话、原始目的地址和性能证据 |
| zmemif over SOCKS5 UDS support | 仅研究 | 后续确认，使用前需要证据 |
| custom xray transport for low-copy handoff | 仅研究 | 后续确认，使用前需要 benchmark 证据 |
| original-destination transparent proxy capture | 研究候选 | 使用前需要双向验证；不得把内核透明劫持包装为 VPP 原生路径 |

## DNS VPP 服务路径

| 项目 | 当前状态 | 处理建议 |
| --- | --- | --- |
| VPP L4 punt socket | 已由 VPP 25.10 镜像验证 | 可精确登记 IPv4/IPv6、UDP/TCP、53 端口；仅作为 VPP 原生服务接口的基础能力，不代表 TCP 会话代理已经完成 |
| VPP DNS 服务接口 | 已实现局部会话闭环 | `dns-vpp-proxy` 以 VCL 建立 IPv4/IPv6 TCP/UDP 53 会话并转交本地 SmartDNS；真实容器报文已验证。任意外部目的地址的透明接管、完整选线与 TTL 业务流仍须单独验收 |
| nftables DNS redirect | 遗留测试路径 | 不得写入生产 RuntimePlan；仅可保留在独立命名空间回归脚本，且测试名称必须标注 legacy |
| DNS over TCP 回注 | 已实现局部会话闭环 | 使用受控 VCL 会话适配器，不以逐包伪响应替代 TCP；IPv4/IPv6 UDP 与 TCP 均已有真实容器报文。外部透明接管和故障语义仍未验收 |

## QoS 增强候选项

| 项目 | 当前状态 | 处理建议 |
| --- | --- | --- |
| VPP classify/record/store/egress-map/mark/policer | 生产基线 | 保留并实现 |
| protected classes token-bucket policer | 生产基线 | 保留并实现 |
| 自研 VPP 智能 QoS 插件 | 产品必需，尚未实现 | 提供整形、工作守恒调度、按流/主机公平和 AQM；必须随 VPP 版本锁定构建、加载读回，并通过双向饱和包流验收后才可启用 |
| CAKE / FQ-CoDel | 行为参考，不直接集成 | 用于公平、AQM、链路开销与验收指标设计；不得将 Linux qdisc 插入生产转发路径 |
| WRED / RED | 不作为默认算法 | 只有在自研插件的实测基线证明优于既定 AQM 后才可选择，不能以名称替代满载证据 |
| 旧版 DPDK HQoS | 禁止作为产品基线 | 上游已弃用；不影响 DPDK 作为 VPP 数据面高性能回退，但不得把它宣称为智能 QoS 实现 |

## 研究进入条件

研究候选只有在产品经理明确纳入范围后才进入工作计划；进入后按功能批次验收。本文的候选状态不会阻塞无关的日常修复。生产业务转发的安全边界仍由产品功能边界和运行时实现决定，不能用研究记录替代运行态证据。
