# Linux 端测试记录

在 AWS 的 Ubuntu 服务器上用**同一份源码**（关键文件 sha256 与 Windows 工作区逐一比对一致）完成构建、单测、裸二进制与 Docker 两条部署链路的实测。

## 环境

| 项 | 值 |
|---|---|
| 系统 | Ubuntu 24.04.4 LTS，kernel 6.17.0-1017-aws，x86_64 |
| 规格 | 2 vCPU / 1.9 GB 内存 / 23 GB 可用磁盘 |
| Go | 1.27.2（`/usr/local/go/bin/go`，root 安装） |
| C 工具链 | gcc 13.3.0、make |
| Docker | 29.1.3（官方镜像 `eceasy/cli-proxy-api:latest`） |
| 系统时区 | **UTC**（`date` → `02:56 UTC`） |
| 出网 | 直连 `https://anyrouter.top/` → 200（无代理） |
| CPA | 8.0.36，commit `20c9095c`（与 Windows 实例、本地源码 HEAD 同一 commit） |

测试全程隔离：`~/cpa-plugin-test/{src,run,docker}`，端口 8319（裸二进制）与 8321（容器），未触碰服务器上任何既有服务（sing-box / argo / sb 等）。

## 构建与产物

```
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "-s -w" -o dist/anyrouter.so .
```

| 检查 | 结果 |
|---|---|
| 产物 | `anyrouter.so`，15,447,168 字节 |
| ABI 符号 | `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown` 全部导出 |
| glibc 需求 | 最高 `GLIBC_2.34`（宿主 2.39、官方镜像 2.36 → 均在兼容范围内） |
| 依赖 | `ldd` 仅 `libc.so.6` + `ld-linux` |
| 装载 | `python3 -c "ctypes.CDLL('dist/anyrouter.so')"` → `DLOPEN OK` |

发布的 zip 已按商店规范生成：`anyrouter_0.1.0_linux_amd64.zip`（库在 zip 根目录），sha256 记入 `dist/checksums.txt`。

## 单元测试

| 项 | 结果 |
|---|---|
| `CGO_ENABLED=0 go test ./...` | **36/36 通过**（Go 1.27.2 / linux-amd64） |
| `CGO_ENABLED=0 go vet ./...` | 无告警 |

跨平台一致：挑战页夹具求解、规范算法兜底、用户 ID 推导、签到分类、额度映射、面板结构与可访问性锚点等全部在 Linux 上同样通过。

## 链路一：裸二进制（端口 8319）

`scripts/verify_live.sh --base-url http://127.0.0.1:8319 --key <key>` → **11 组检查全部通过**。

| # | 检查 | Linux 实测 |
|---|---|---|
| 1 | 二进制/开关/注册 | `X-CPA-SUPPORT-PLUGIN=1`，`configured/enabled/registered/effective_enabled` 全 `true`，`path=plugins/anyrouter.so`，14 个配置字段、菜单正确 |
| 2 | 资源页与鉴权边界 | 面板无密钥 200（47023 字节，自包含）；管理路由无密钥 **401** |
| 3 | 状态 | `host_available=true`，`base_url` 来自配置 |
| 4 | 挑战页（经宿主 transport） | **从美国机房 IP 直接解出**：`method=js#1`，按 host 缓存 |
| 5 | 账号导入 | 写入凭证文件；列表只回掩码；单账号接口可取完整 Cookie |
| 6 | 凭证被认领 | `provider=anyrouter type=anyrouter auth_index=…` |
| 7 | 签到链路 | 占位 Cookie → `failed` +「签到接口拒绝该 Cookie（HTTP 401）：{}」 |
| 8 | 余额链路 | `failed` +「余额接口拒绝该 Cookie（HTTP 401）：无权进行此操作，未登录且未提供 access token」 |
| 9 | 配额链路 | `GET /v8/management/plugins/anyrouter/quota?auth_index=…` → 200（本次无可读额度，返回空响应，符合"额度查询失败不报错"的设计） |
| 10 | 停用/启用 | 凭证文件被改写并即时生效 |
| 11 | 事件 | 事件流记录了账号/签到/余额/配额操作 |

补充核对：宿主写入的凭证文件权限为 **0600**（`-rw------- ubuntu:ubuntu`），内容只含 `type/provider/remark/cookie/disabled`；插件日志无 panic、无 `fuse`。

## 链路二：官方 Docker 镜像（端口 8321）

镜像 `eceasy/cli-proxy-api:latest` = **v8.0.36**，基底 Debian 12 bookworm（glibc 2.36，TZ=Asia/Shanghai），插件目录挂载到 `/CLIProxyAPI/plugins`。

| 步骤 | 结果 |
|---|---|
| 容器内加载 | 日志出现 `pluginhost: plugin loaded plugin_id=anyrouter path=plugins/anyrouter.so` 与 `plugin registered` → **glibc 兼容性得到实证**（插件要求 2.34 ≤ 镜像 2.36） |
| 管理 API（宿主访问） | 初次 `{"error":"remote management disabled"}`（**复现了容器坑**：`management.allow-remote` 默认 false，宿主经 Docker 网桥被视作远端）；改为 `true` 后 `plugins_enabled=true`、`anyrouter registered=true effective=true` |
| 资源页 | 无密钥 200，且 HTML 的 sha256 与裸二进制实例**完全一致**（`66b73fde…`）→ 同一份插件产物在两个环境表现一致 |
| 全量检查 | `verify_live.sh --base-url http://127.0.0.1:8321` → **11 组全部通过**（含容器内解挑战页 `method=js#1`） |

## 真实账号实测（美国机房 IP，无需代理）

经用户同意，把 Windows 上的凭证文件复制到服务器后实测：

| 项 | 结果 |
|---|---|
| 签到 | `already`｜今日已签到 —— 会话从美国 IP 使用被站点接受 |
| 余额 | **$2,290.95 剩余 / $109.05 已用 / 216 次请求**（原始 quota 1145472588 / used 54527412），与 Windows 上完全一致 |
| 用户 ID | `170543`，仍由会话 Cookie 自动推导（new-api 的 `New-Api-User` 头） |
| 出口 | 宿主 transport（未配 `proxy_url`，Linux 主机直连 anyrouter.top 通） |

结论：**美国服务器不需要代理**；国内直连不通的那台（Windows）走的是 CPA 的 `requests.proxy-url`，实测同样正常。

## 代理支持（本次新增，v0.1.0 之后）

宿主 `host.http.do` 没有"按请求传代理"的入口（`pluginapi.HTTPRequest` 只有 Method/URL/Headers/Body/WireProfile），代理只在宿主内部按 **请求级 → 凭证级 → 配置级 `requests.proxy-url`** 解析。因此：

| 配置 | 行为 |
|---|---|
| `proxy_url` 留空（默认） | 走宿主 `host.http.do`，**自动继承 CPA 的 `requests.proxy-url`**（推荐：代理、请求日志、TLS 策略都在一处） |
| 插件级 / 账号级 `proxy_url` | 插件改用自有 `net/http` 客户端走该代理（`http/https/socks5/socks5h`），启动后日志记一行"已按插件配置使用自有 HTTP 客户端…不经过宿主日志与传输策略" |
| `direct` / `none` | 强制直连，忽略所有代理 |

关键正确性细节：**挑战页与 API 请求始终走同一出口**——`acw_sc__v2` 按出口 IP 签发，缓存键是 `host|出口`，不同出口不会串用彼此的 Cookie。账号级 `proxy_url` 覆盖插件级。单测覆盖解析（含非法 scheme/host）、优先级（账号 > 插件 > 宿主）、出口隔离与三种 transport 选择。

## 长期部署（容器 + 自动重启）

用户选择"先把服务跑起来，不对接端口"，因此只绑宿主机回环、不碰 AWS 安全组。

```
~/anyrouter-cpa/
├── config.yaml            v8 布局：host ""、port 8317、allow-remote true、随机管理密钥、oauth.auth-dir /root/.cli-proxy-api
│                          插件：auto_checkin true、checkin_time 08:00、timezone Asia/Shanghai、auto_refresh_balance true、proxy_url ""
├── plugins/anyrouter.so   最终产物
├── auths/                 真实账号凭证（0600）
├── MANAGEMENT-KEY.txt     管理密钥（0600）
├── API-KEY.txt            CPA 客户端密钥（0600）
└── docker-compose.yml     备用（本机没装 compose 插件，实际用 docker run）
```

实际启动方式：

```bash
docker run -d --name anyrouter-cpa --restart unless-stopped \
  -e TZ=Asia/Shanghai -p 127.0.0.1:8317:8317 \
  -v ~/anyrouter-cpa/config.yaml:/CLIProxyAPI/config.yaml \
  -v ~/anyrouter-cpa/plugins:/CLIProxyAPI/plugins \
  -v ~/anyrouter-cpa/auths:/root/.cli-proxy-api \
  eceasy/cli-proxy-api:latest
```

验证结果：`registered=true effective=true`、面板 200（47696 字节）、调度 `started=true auto_checkin=true checkin_time=08:00 auto_balance=true`、`next_checkin_at=2026-10-22T08:00:00+08:00`、真实账号余额与签到均成功、事件流有记录；`docker restart` 后自动恢复并重新注册。

### 部署时踩到的两个坑（都已写进 README 排障）

1. **容器里 `server.host` 必须绑所有接口**。我先按裸机习惯写了 `host: "127.0.0.1"`，容器内只监听自己的 loopback，宿主经 `-p` 映射进来直接 `Connection reset by peer`；改成 `host: ""` 后正常（公网仍由 `-p 127.0.0.1:8317:8317` 限制）。
2. **容器内 `management.allow-remote` 默认 false**，宿主访问管理 API 得到 `403 remote management disabled`。本部署端口只绑回环，因此打开它是安全的（这正是 skill 记录的容器坑）。

## 源码与部署的真相来源（GitHub）

工作区、GitHub 与服务器三处的唯一真相是仓库 `main` 分支：<https://github.com/qizhuxu/anyrouter-cpa-plugin>。

- 服务器侧用 `scripts/deploy_linux.sh` 一条命令重拉重部署：`REPO_URL=… ./deploy_linux.sh`（fetch+ff-only → `go test` → `cgo` 构建 + `dlopen` 自检 → 安装到 `plugins/` → `docker restart` → 打印注册日志）。已实测幂等（第二次是 `Already up to date` 后照常重建重启）。
- 仓库不含二进制（`.gitignore` 排除 `dist/`、`*.so`、`*.dll`），服务器自己构建，避免产物与源码不一致；`.gitattributes` 强制 LF，避免 Windows 提交的 `.sh` 在 Linux 上因 CRLF 失败。
- 部署目录的 `config.yaml` 与插件状态文件不进仓库：前者含管理密钥与路径差异，后者是面板托管设置的运行态。

## 自动化任务（面板托管）

`scheduler` 每次 tick 读**生效配置**（配置文件 + 面板覆盖），因此面板改动下一分钟生效、无需重启。面板覆盖写在插件状态文件（部署里指向挂载的 `/CLIProxyAPI/plugins/anyrouter-state.json`，容器重建不丢）；`{"reset":true}` 恢复跟随配置。实测：把签到改为 `09:30`、余额间隔 `40` 分钟 → `source=panel`、`next_checkin_at` 随之变化、状态文件落盘；提交 `25:99` 被拒；重启后覆盖仍生效。

## 与 Windows 端的差异（实测，值得记住）


| 项 | Windows | Linux |
|---|---|---|
| 动态库替换 | 宿主从**影子副本**加载，磁盘文件被锁但可覆盖，需重新加载/重启 | 直接 `dlopen` 原路径；文件可原地覆盖，同样需要重启进程才生效 |
| 上游 401 报文 | 余额：`未提供 New-Api-User` | 余额：`未登录且未提供 access token`；签到 401 体为 `{}` |
| 时区 | 跟随本机 | 裸二进制跟随 `TZ`（本机为 **UTC**）；官方镜像内是 `Asia/Shanghai` |

上游报文差异不影响行为：插件的失败分类以 HTTP 状态码为准，原文只作为提示透出（这正是上次把"缺头"误报成"Cookie 过期"后加的改进）。**时区差异需要显式配置**：在 UTC 主机上保持 `timezone: Local`，`checkin_time: 08:00` 会在 08:00 UTC（= 16:00 北京）触发；建议直接写 `timezone: "Asia/Shanghai"`。

## 未覆盖

- systemd / 开机自启（部署用的是容器 `--restart unless-stopped`，已实测重启自恢复）、arm64 产物、`prefers-reduced-motion` 的媒体模拟。
- 服务器上 2 核 1.9G 跑 CPA 的并发压力（本次只做功能验证）。
- 端口对外暴露：用户选择"先不对接"，因此只绑 `127.0.0.1:8317`，公网不可达；需要对外时自行在 AWS 安全组放行并调整 `-p`。

## 复现

```bash
# 服务器上（源码已在 ~/cpa-plugin-test/src）
export PATH=/usr/local/go/bin:$PATH
cd ~/cpa-plugin-test/src && CGO_ENABLED=0 go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "-s -w" -o dist/anyrouter.so .

# 裸二进制实例（配置与凭证都在 ~/cpa-plugin-test/run）
cd ~/cpa-plugin-test/run && ./cli-proxy-api --config config.yaml
# 容器实例
docker run -d --name anyrouter-docker-test -p 127.0.0.1:8321:8319 \
  -v ~/cpa-plugin-test/docker/config.yaml:/CLIProxyAPI/config.yaml \
  -v ~/cpa-plugin-test/docker/plugins:/CLIProxyAPI/plugins \
  -v ~/cpa-plugin-test/docker/auths:/root/.cli-proxy-api \
  eceasy/cli-proxy-api:latest

# 两端都用同一个脚本验证（Linux / macOS 通用）
~/cpa-plugin-test/verify_live.sh --base-url http://127.0.0.1:8319 --key anyrouter-verify-key
~/cpa-plugin-test/verify_live.sh --base-url http://127.0.0.1:8321 --key anyrouter-verify-key
```
