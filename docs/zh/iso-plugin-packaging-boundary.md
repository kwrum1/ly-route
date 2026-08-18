# ISO 插件打包边界

> 仅适用于 rootfs/ISO 发布构建，不是日常热修复门禁。热修复先替换受影响插件并按[开发、热修复与验收流程](development-workflow.md)验证。

本规则是网关 ISO 的硬边界，用来防止“控制面程序已编译，但 VPP 插件漏入固件”的情况。

## 适用范围

- 网关：`rootfs → 磁盘镜像 → 安装 ISO` 必须携带并验证以下 VPP 插件。
- 编排器：只携带编排器插件，不得把网关的 PPPoE、Smart QoS、安全插件混入。
- 本规则同时适用于 amd64 和 arm64；插件必须与目标架构及 rootfs 中的 VPP 版本一致。

## 网关必选插件

| 运行包 | 必须存在的 VPP 插件 |
|---|---|
| `ly-route-vpp-pppoe-client` | `ly_route_pppoe_client_plugin.so` |
| `ly-route-vpp-smart-qos` | `ly_route_smart_qos_plugin.so` |
| `ly-route-vpp-security-guard` | `ly_route_security_guard_plugin.so` |

网关还必须携带以下运行时适配组件；它们不是 VPP `.so`，但缺少任意一个
同样会导致安装后功能不完整：

| 运行包/依赖 | 必须存在的运行内容 |
|---|---|
| `ly-route-dns-vpp-proxy` | `/usr/lib/ly-route/ly-route-dns-vpp-proxy`、`ly-route-dns-vpp-proxy-v6` 及对应 systemd 服务 |
| `ly-route-vpp-apply` | `/usr/lib/ly-route/vpp-apply` |
| `vpp` | `/usr/bin/vpp`、`/usr/bin/vppctl` |
| `smartdns`、`kea-dhcp4-server`、`xray`、`ipset` | 对应运行程序和产品服务依赖 |

插件目录必须分别为：

- amd64：`/usr/lib/x86_64-linux-gnu/vpp_plugins/`
- arm64：`/usr/lib/aarch64-linux-gnu/vpp_plugins/`

PPPoE 的 `/usr/lib/ly-route/ly-route-pppoe-client` 是控制程序，不能替代
`ly_route_pppoe_client_plugin.so`。

## 构建门禁

1. `build-runtime-debs.sh` 必须为目标架构生成网关全部运行包。
2. `build-rootfs.sh` 必须检查全部网关运行包已安装，检查三个 `.so` 为非空文件，
   并检查 DNS 适配器、VPP 控制程序及必要服务文件。
3. `build-disk-image.sh` 解包 rootfs 后再次检查运行包、运行文件、服务单元和三个
   `.so`，再写入镜像清单 `runtime_packages`、`runtime_files`、`runtime_units`、
   `runtime_plugins`。
4. `build-auto-install-iso.sh` 只接受带有完整四类清单的网关镜像；缺少任意插件、
   适配器或服务依赖时直接失败，不创建 ISO。
5. 安装后的 `ly-route-runtime-check.service` 必须检查 `vppctl show plugin`、
   对应 CLI、实际运行能力及服务依赖；静态文件存在不能作为通过证据。
6. 物理机安装前后必须额外检查网卡驱动、PCI/MAC 映射、管理口排除、数据口所有权、
   VPP 接口和插件架构一致性。ESXi 的 VMXNET3/AF_PACKET 结果不能替代这些证据。
7. 每次新构建先删除输出目录中旧的 Ly Route ISO 及其校验/清单文件，不能复用
   旧 ISO、旧镜像或旧串口日志。

## 发布前检查

```bash
./scripts/build-runtime-debs.sh all
./scripts/build-rootfs.sh --product gateway --arch amd64
./scripts/build-disk-image.sh --product gateway --rootfs <rootfs>
./scripts/build-auto-install-iso.sh --product gateway --image <image>
```

安装后至少保留以下证据：

```bash
vppctl show plugin
systemctl status ly-route-runtime-check.service
cat /usr/share/ly-route/artifact-manifest.json
```

任一插件门禁失败，网关 ISO 只能标记为失败，不能进入安装验收或生产发布。
