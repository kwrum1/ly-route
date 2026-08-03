# VPP 原生业务服务路径

## 不变量

Gateway 和 Orchestrator 的业务包从业务口进入后，分类、策略、选线、NAT、服务链和回程选择都必须留在 VPP 数据面。`nftables`、TProxy 和 Linux policy routing 不得成为业务透明劫持或转发回退路径。

Linux 仍可保留管理面、防护规则、安装诊断和独立回归命名空间；这些用途不能被计入生产业务数据面的验收证据。

## DNS

LAN 的 IPv4/IPv6 TCP/UDP 53 由 VPP 在入口分类。VPP 只将已分类 DNS 交给受控 DNS 服务接口，其他报文继续走 VPP；DNS 服务的结果通过同一受控接口回注 VPP，再按 DNS 高优先级和 TTL 关联策略转发。

`dns-vpp-proxy` 已作为版本锁定的 VCL 会话适配器接入：它在 VPP 建立 IPv4/IPv6 TCP/UDP 53 监听会话，并用独立的原生套接字将 DNS 负载转交本地 SmartDNS。容器中的真实 UDP、TCP、IPv4 和 IPv6 请求均已得到 SmartDNS 回应；该实现不依赖 nftables、TProxy 或 Linux policy routing。

生产 RuntimePlan 不得生成 nftables DNS redirect。当前适配器仅证明 VPP 本地 DNS 服务会话，不等同于任意原始目的地址的透明截获；在后者具有独立的 VPP 规则、原始目的地址保持和完整包流证据前，不能把该能力标记为验收完成。DNS 规则仍必须遵守首匹配、DNS 优先于普通策略路由、TTL 继承、失败 `NOERROR/NODATA` 和无回落的既有契约。

## 代理

代理逻辑 WAN 的规则、underlay 选择和 VPP ABF 留在 VPP。Xray 等用户态进程只能作为受控服务节点，通过版本锁定服务接口收发已经由 VPP 选择的会话；其失效必须导致该代理动作失败关闭，不能改由 nftables/TProxy 偷偷接管。

## 验收门禁

生产启用前必须同时保存以下证据：

1. VPP 接口、分类/服务转交和回注的应用读回与计数器。
2. 独立容器 LAN 客户端的 TCP 与 UDP DNS pcap，证明外部 53 没有直发包。
3. DNS 固定 WAN、普通 PBR 冲突、TTL 到期、上游失效和代理 DNS 的完整包流。
4. 代理会话的正反向 pcap、原始目的地址保持、节点失效失败关闭和恢复。
5. 不存在 nftables/TProxy/Linux policy routing 业务规则的产品根文件系统检查。
