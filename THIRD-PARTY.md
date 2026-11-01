# 第三方组件与许可证

Ly Route Gateway 的安装镜像与升级包包含下列第三方组件。各组件的版权归其各自
权利人所有，并按其各自许可证授权；[EULA](EULA.md) 不改变这些许可证赋予您的权利。

## 组件清单

| 组件 | 版本 | 许可证 | 上游源码 | 是否修改 |
| --- | --- | --- | --- | --- |
| Debian GNU/Linux | 12 (bookworm) | 多种自由软件许可证 | https://www.debian.org/ | 否（使用发行版包） |
| Vector Packet Processing (VPP) | 26.06 | Apache-2.0 | https://github.com/FDio/vpp | 否（内核与基础插件未修改） |
| Pingora | 0.9.0 | Apache-2.0 | https://github.com/cloudflare/pingora | 否（作为 Rust 依赖链接） |
| SmartDNS | 1.20261019 | GPL-3.0 | https://github.com/pymumu/smartdns | 否（使用上游构建） |
| dnsmasq | 2.89（Debian 12） | GPL-2.0 / GPL-3.0 | https://thekelleys.org.uk/dnsmasq/doc.html | 否 |
| Xray-core | 26.10.29 | MPL-2.0 | https://github.com/XTLS/Xray-core | 否（使用上游 release 二进制） |
| nftables | Debian 12 版本 | GPL-2.0 | https://www.netfilter.org/projects/nftables/ | 否 |
| iptables | Debian 12 版本 | GPL-2.0 | https://www.netfilter.org/projects/iptables/ | 否 |
| ipset | Debian 12 版本 | GPL-2.0 | https://ipset.netfilter.org/ | 否 |
| OpenSSL | 3.0.x（Debian 12） | Apache-2.0 | https://www.openssl.org/ | 否 |
| Go runtime | 随构建工具链锁定 | BSD-3-Clause | https://go.dev/ | 否（静态链接） |
| Linux kernel | Debian 12 内核 | GPL-2.0 | https://www.kernel.org/ | 否 |
| systemd | Debian 12 版本 | LGPL-2.1-or-later | https://systemd.io/ | 否 |
| Chromium（仅安装 ISO 的 Live 环境） | Debian 12 版本 | BSD-3-Clause 等 | https://www.chromium.org/ | 否 |
| Xorg（仅安装 ISO 的 Live 环境） | Debian 12 版本 | MIT/X11 等 | https://www.x.org/ | 否 |
| Rust crates（Pingora 依赖） | 由 `Cargo.lock` 锁定 | MIT / Apache-2.0 为主 | https://crates.io/ | 否 |

## 产品自有组件

下列组件由 Ly Route 开发，属于闭源产品代码，不属于第三方组件：

- `ly-route-control` 控制面与 API；
- `ly-route-edge`（基于 Pingora 的 Web/API 前端）；
- VPP 插件：`ly_route_pppoe_client_plugin.so`、`pppoeclient_plugin.so`、
  `ly_route_security_guard_plugin.so`、`ly_route_dns_intercept_plugin.so`、
  `ly_route_pre_nat_route_plugin.so`、`ly_route_smart_qos_plugin.so`；
- `ly-route-vpp-apply`、`ly-route-dns-vpp-proxy` 等运行组件；
- Web 管理界面与安装器。

这些组件通过进程边界、VPP 插件 ABI 或系统调用与第三方组件交互，不改变第三方
组件的许可证。

## GPL / LGPL 组件合规

- **SmartDNS**、**dnsmasq**、**nftables**、**iptables**、**ipset**、**Linux kernel**
  以独立进程或内核模块形式使用，未与产品代码静态链接。
- 对应源码可从表中上游地址获取；我们分发的是未修改的上游构建。
- 如上游已不再提供对应版本，可通过本仓库 Issues 联系我们，获取构建时使用的
  源码快照或书面要约（有效期自获取产物之日起三年）。

## MPL-2.0 组件合规

- **Xray-core** 以独立进程运行，使用上游 release 二进制，未修改。
- MPL-2.0 覆盖文件（如有）的源码可从上游获取；我们对 Xray-core 的修改（如有）
  将在本仓库或上游 fork 中公开。

## Apache-2.0 组件合规

- **VPP**、**Pingora**、**OpenSSL** 的版权与许可证声明随附于
  [`licenses/Apache-2.0.txt`](licenses/Apache-2.0.txt)。
- 我们未修改 VPP 内核或基础插件；产品自有 VPP 插件单独分发，不声称属于 VPP 项目。

## 许可证全文

| 许可证 | 文件 |
| --- | --- |
| Apache-2.0 | [`licenses/Apache-2.0.txt`](licenses/Apache-2.0.txt) |
| GPL-2.0 | [`licenses/GPL-2.0.txt`](licenses/GPL-2.0.txt) |
| GPL-3.0 | [`licenses/GPL-3.0.txt`](licenses/GPL-3.0.txt) |
| MPL-2.0 | [`licenses/MPL-2.0.txt`](licenses/MPL-2.0.txt) |
| BSD-3-Clause | [`licenses/BSD-3-Clause.txt`](licenses/BSD-3-Clause.txt) |

其他许可证（MIT、LGPL、Artistic 等）可从对应上游项目获取。

## 联系方式

第三方许可证或源码相关请求：通过本仓库 GitHub Issues 提交，标题注明
`THIRD-PARTY`。
