# API 参考

适用版本：**1.2**。控制面 API 由设备本机提供，默认随 Web 控制台一起监听 443。

```text
https://<管理地址>/api/v1
```

## 1. 认证

登录成功后服务端下发会话 Cookie（`ly_route_session`，HttpOnly），后续请求携带该 Cookie 即可。

```bash
curl -k -c /tmp/ly.cookie -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"密码"}' \
  https://管理地址/api/v1/auth/login
```

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/login` | 登录，返回会话信息与 `password_change_required` |
| POST | `/auth/logout` | 退出登录，会话立即失效 |
| GET | `/auth/session` | 查询当前会话与角色 |
| POST | `/auth/change-password` | 修改当前账号密码 |
| GET/POST | `/auth/users` | 账号列表 / 新建账号（仅管理员） |
| PATCH/DELETE | `/auth/users/{id}` | 修改 / 删除账号（仅管理员） |

角色：

- `admin`：可读写；
- `readonly`：只读，所有写操作返回 `403 forbidden`。

首次登录后未修改初始密码时，除改密与登出外的写操作会被拒绝。

## 2. 通用约定

- 请求与响应均为 `application/json; charset=utf-8`。
- 写操作需要管理员角色。
- 错误响应：

```json
{"code":"invalid_payload","message":"...","errors":[],"request_id":"..."}
```

- 常见状态码：`400` 参数错误、`401` 未登录、`403` 权限不足、`404` 对象不存在、`409` 对象冲突、`422` 业务校验失败、`503` 服务暂不可用。

## 3. 资源接口

大多数配置资源遵循同一套形式：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/资源集合` | 列表 |
| POST | `/资源集合` | 新建 |
| GET | `/资源集合/{id}` | 详情 |
| PATCH | `/资源集合/{id}` | 修改 |
| DELETE | `/资源集合/{id}` | 删除 |

保存只写入期望配置；需要调用 `/config/apply` 才会下发到设备。

### 3.1 系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/product` | 产品信息 |
| GET | `/health` | 健康状态与组件依赖 |
| GET | `/capabilities` | 当前设备支持的能力 |
| GET | `/mode` | 运行模式 |
| POST | `/mode/initialize` | 初始化运行模式 |
| GET/POST | `/management/network` | 查询 / 修改管理口地址 |

### 3.2 网络

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/interfaces` | 物理接口列表 / 修改 |
| GET/POST | `/interface-bonds` | 聚合组 |
| GET/POST | `/gateway/lan-lines` | LAN 线路 |
| GET/POST | `/gateway/wan-links` | WAN 线路 |
| GET/POST | `/gateway/wan-groups` | WAN 群组 |
| GET | `/gateway/pppoe/status` | PPPoE 状态 |
| POST | `/gateway/pppoe/connect` | 主动拨号 |
| POST | `/gateway/pppoe/disconnect` | 断开拨号 |

### 3.3 策略

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/gateway/policies/routes` | 策略路由 |
| GET/POST | `/gateway/nat/port-maps` | 端口映射 |
| GET/POST | `/gateway/nat/static` | 一对一静态 NAT |
| GET/POST | `/flow-control/policies` | 安全流控规则 |
| GET | `/flow-control/runtime` | 流控能力与运行状态 |
| GET/POST | `/objects/groups` | 对象组（域名组 / IP 组） |
| GET/POST | `/objects/ip-groups` | IP 组 |

### 3.4 DNS

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/dns/upstreams` | 解析上游 |
| GET/POST | `/dns/policies` | DNS 策略 |
| GET/POST | `/dns/domain-ip-sets` | 域名对应地址集合（自动维护） |
| GET | `/dns/proxy-endpoints` | 代理解析端点 |
| POST | `/dns/resolve` | 按策略解析指定域名（排障用） |

### 3.5 DHCP

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/dhcp/servers` | DHCP 服务 |
| GET/POST | `/dhcp/static-bindings` | 静态地址分配 |
| GET | `/dhcp/leases` | 当前租约 |
| DELETE | `/dhcp/leases/{id}` | 释放租约 |

### 3.6 代理

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/proxy/egresses` | 代理出口 |
| GET/POST | `/proxy/nodes` | 代理节点 |
| GET/POST | `/proxy/subscriptions` | 代理订阅 |
| GET/POST | `/proxy/groups` | 节点组 |
| GET | `/proxy/xray/status` | 运行状态 |
| POST | `/proxy/xray/restart` | 重启代理服务 |
| GET | `/proxy/xray/logs` | 最近日志 |

### 3.7 运行与配置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/runtime/preview` | 只读预览将要下发的配置 |
| GET | `/runtime/status` | 运行时组件状态 |
| GET | `/runtime/apply/state` | 最近一次应用状态 |
| POST | `/config/apply` | 应用当前配置 |
| GET | `/config/apply/status` | 查询应用进度 |
| GET | `/config/dependencies` | 对象引用关系 |
| GET | `/config/export` | 导出配置 |
| POST | `/config/import` | 导入配置 |
| GET | `/config/snapshots` | 快照列表 |
| POST | `/config/snapshots` | 创建快照 |
| POST | `/config/snapshots/{id}` | 恢复快照 |
| POST | `/config/factory-reset` | 配置初始化（需 `RESET`） |
| GET | `/firmware/update/status` | 升级状态 |
| POST | `/firmware/update/stage` | 上传并校验升级包 |
| POST | `/firmware/update/install` | 开始安装 |

### 3.8 监控

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/dashboard/summary` | 总览数据 |
| GET | `/telemetry/dashboard` | 仪表盘指标 |
| GET | `/telemetry/interfaces` | 接口流量 |
| GET | `/telemetry/traffic-trend` | 流量趋势（`window`、`points`） |
| GET | `/telemetry/sessions` | 活动会话 |
| GET | `/telemetry/top-sessions` | Top 会话 |
| GET | `/telemetry/top-domains` | Top 域名 |
| GET | `/telemetry/online-users` | 在线用户 |
| GET | `/telemetry/policy-hits` | 策略命中 |
| GET | `/telemetry/audit-events` | 审计事件 |
| POST | `/diagnostics/ping` | 连通性诊断 |
| GET | `/dhcp/leases` | DHCP 租约 |

> `/api/v1/internal/*` 为设备内部接口，仅供本机组件使用，不对外提供。

## 4. 示例

### 4.1 查询健康状态

```bash
curl -ks https://管理地址/api/v1/health
```

```json
{"status":"ok","version":"1.2","degraded":false,"dependencies":[{"name":"vpp","available":true,"state":"available"}]}
```

### 4.2 创建默认上网策略

```bash
curl -k -b /tmp/ly.cookie -H 'Content-Type: application/json' \
  -d '{
        "name": "默认上网",
        "priority": 100,
        "enabled": true,
        "action": "nat",
        "match": {"sources": [], "destinations": [], "protocols": ["any"]},
        "egress": "<WAN 线路 ID>"
      }' \
  https://管理地址/api/v1/gateway/policies/routes
```

### 4.3 应用配置并等待结果

```bash
# 提交应用，返回 202 与 transaction_id
curl -k -b /tmp/ly.cookie -X POST https://管理地址/api/v1/config/apply

# 查询进度
curl -k -b /tmp/ly.cookie \
  "https://管理地址/api/v1/config/apply/status?transaction_id=<transaction_id>"
```

```json
{"status":"applied","runtime_state":"converged","reason":"","transaction_id":"txn-...","http_status":200}
```

同一时间只允许一个应用事务；已有事务执行时会返回 `apply_in_progress`。

### 4.4 PPPoE 拨号

```bash
curl -k -b /tmp/ly.cookie -X POST https://管理地址/api/v1/gateway/pppoe/connect
curl -k -b /tmp/ly.cookie https://管理地址/api/v1/gateway/pppoe/status
```
