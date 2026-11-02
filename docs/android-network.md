# Android 网络调研与实现边界

## 为什么使用 root TUN

Android 同一用户或工作资料仅允许一个活动 VpnService；第二个 VPN 会替换第一个。[Android VPN 开发文档](https://developer.android.com/develop/connectivity/vpn) 描述了这一生命周期。

Shellscaled 直接通过 root 打开 `/dev/net/tun`，由 tailscaled 管理 `tailscale0`，不会向 Android 注册第二个 VpnService。只避免占用 VPN 槽位还不够：Android 使用 netd 的 UID、网络绑定、fwmark 和策略路由选择网络，必须同时适配这些规则。

## fwmark 与受保护 socket

[AOSP Fwmark.h](https://android.googlesource.com/platform/system/netd/+/android-17.0.0_r1/include/Fwmark.h) 将低 16 位用于 netId，第 16 位用于显式网络选择，第 17 位用于 `protectedFromVpn`，第 18–19 位用于权限，第 20 位用于计费标记。不能直接照搬桌面 Linux 的 Tailscale mark 分配。

Android 移植将自有 mask 放在 `0x1e000000`，tailscaled 外层传输 socket 使用 `SO_MARK=0x10020000`，包含自有 bypass 位和 Android 的受保护位。table 52 的后备规则匹配 `fwmark 0x0/0x10030000`：自有 bypass 位、显式选网位（16）或受保护位（17）任一置位都会跳过此规则，继续使用 Android 的路由。原生 VPN App 通常通过 [VpnService.protect](https://developer.android.com/reference/android/net/VpnService#protect(int)) 请求同类保护，root daemon 使用 socket mark 完成。

模块更新检查使用 `SO_MARK=0x10000000`，只避开 Shellscaled 自己的 TUN 路由，不设置 Android 的 `protectedFromVpn` 位。因此 GitHub 请求仍可经由用户已启用的 Android VPN；只有 tailscaled 自身的外层传输使用完整的 `0x10020000` 保护标记。

[Linux fib_rules.c](https://github.com/torvalds/linux/blob/master/net/core/fib_rules.c) 的 mark 匹配对 XOR 结果施加 mask，规则的 `not` 反转整个谓词；因此测试必须同时覆盖普通 socket、携带 netId 的 socket 及 daemon 外层 socket，不能只比较常量。

## Android 策略路由

[Android 17 RouteController.h](https://android.googlesource.com/platform/system/netd/+/android-17.0.0_r1/server/RouteController.h) 定义 secure VPN 基础优先级 13000、禁止非 VPN 规则 14000、显式网络 16000，以及 bypassable VPN 的 24000 / 27000 等基础优先级。[RouteController.cpp](https://android.googlesource.com/platform/system/netd/+/android-17.0.0_r1/server/RouteController.cpp) 的 VPN 规则还包含 UID 范围、`iif lo`、protected / explicit mark 谓词和 subpriority；不能只比较一个优先级数字。厂商实现也可能不同，验收时须读取实际规则。

本移植保留 table 52，在优先级 12500 匹配 Tailnet 的 `100.64.0.0/10` 和 `fd7a:115c:a1e0::/48`，并排除自有 bypass mark。该规则早于 Android 17 secure VPN 的基础优先级。13001 后备规则只匹配 `fwmark 0x0/0x10030000`；它早于 bypassable VPN 基础优先级，且不能笼统认为晚于所有带 subpriority 的 secure VPN 规则。查表没有目标路由时继续使用 Android 后续规则；未选择出口节点时，该表没有互联网默认路由。

这意味着普通 Tailnet 地址和其他子网路由具有不同优先级。显式绑定到网络的 socket、更高优先级的 UID 规则、VPN lockdown 或其他 root 模块可能影响结果；不能据此推断所有 VPN 都兼容。出口节点也需要单独验收，不能从普通 Tailnet 共存测试推广。

原 Android fork 的实现存在 IPv4 / IPv6 规则重复遍历、命令回退遗漏 `Dst` / `Invert`、规则恢复监控仍使用 Linux 优先级范围等问题。当前已统一地址族筛选和命令 / netlink 参数，并按实际 Android 优先级识别删除事件；规则清理仍限定本模块的规则形状和 table，不全局 flush。

模拟器进一步发现上游 netmon 只订阅 IPv4 rule 组，且复用 route 消息解析器解码 FIB rule。Android 的 `FRA_UID_RANGE` 与 route 属性编号冲突，会产生解析错误。当前改为订阅 IPv4 / IPv6 rule 事件，按 `fib_rule_hdr` 和 `FRA_PRIORITY` / `FRA_TABLE` 解码，忽略无关属性，并保留完整 32 位表编号。新增回归测试及真实删除 / 恢复测试已覆盖该问题。

## DNS、防火墙与权限

默认 `accept-dns=false`，DNS 配置器拒绝接管系统 DNS，不使用旧 fork 的全局 UDP 53 DNAT。保留 netd、Private DNS 与已有 VPN 的解析策略。CGO 构建使用 Android bionic 解析，避免依赖不存在的 Linux `/etc/resolv.conf`。因此本期不提供系统 MagicDNS，也不宣称实现 Tailscale 的分域 DNS。

默认 `netfilter-mode=off`，避免向 Android 的防火墙链插入桌面 Linux 规则。网络角色操作保留现有 NetfilterMode，严格反向路径检查设备的限制见“出口节点”一节。普通本机 Tailnet 连通与转发场景分别验收；没有转发验证不能宣称热点、子网路由器或出口节点可用。

[Android SELinux 文档](https://source.android.com/docs/security/features/selinux) 明确指出 root 也受强制访问控制约束。模拟器测试保持 enforcing，并检查 AVC；不能用切换 permissive 代替权限适配。

## 出口节点

### 路由

选择获批准的出口节点后，tailscaled 在 table 52 计算 IPv4 / IPv6 默认路由；下文 IPv6 开关可以移除 IPv6 默认路由。默认路由存在时，13001 后备规则只接管未显式绑定网络、未受保护且不含自有 bypass 位的流量；匹配更早的 Android UID / VPN 规则的流量仍按该规则处理。显式绑定或 protected socket、tailscaled 自身 `0x10020000` socket 保留 Android 路由。出口节点离线时隧道内流量可能不可达，不能把规则安装视为端到端连接成功，也不保证所有流量都经过出口节点。

`exit-node-allow-lan-access` 开启时，本地接口前缀可以成为 table 52 的 throw 路由，查表失败后继续 Android 后续规则；关闭时，上游可能把这些前缀送入出口节点。它不会改变显式选网、protected 或 UID 规则。[Android 17 NetworkController](https://android.googlesource.com/platform/system/netd/+/android-17.0.0_r1/server/NetworkController.cpp) 和 [DnsResolver](https://android.googlesource.com/platform/packages/modules/DnsResolver/+/android-17.0.0_r1/res_send.cpp) 会给网络解析请求的 UDP / TCP socket 设置受保护 DNS mark；新版后备 mask 排除这类流量，不能再把 Android 网络 DNS 笼统视为出口节点流量。

### NetfilterMode

出口节点的路由配置与 NetfilterMode 是不同设置。上游 on 模式会安装完整 `ts-input` / `ts-forward` 及 connmark 规则，并写入 `src_valid_mark`；其中拒绝来自非 tailscale 接口的 `100.64.0.0/10` 源地址规则可能影响运营商 CGNAT 网络。不能为获得 connmark 支持而自动开启整套桌面防火墙。

选用或停用出口节点时，helper 只修改 `ExitNodeID` / `ExitNodeIP` 及其 mask，保留 NetfilterMode 和其他偏好。严格 rp_filter 设备可能在 table 52 有默认路由时丢弃隧道回包：helper 检查各接口的 `max(all, iface)`，任一接口为 1 且现有 NetfilterMode 不为 2 时，在 PATCH 前返回明确的不支持提示。已由用户配置为 2 的设备允许继续，但不会改变该设置。模块不会自行修改全局 sysctl / iptables；首次初始化和命令行白名单仍要求 `netfilter-mode=off`。严格 rp_filter 设备的完整兼容方案尚未提供。

### 应用前检查

已选节点离线或缺少可用出口路由时，分配到出口隧道的流量可能不可达。helper 在 PATCH 前从 `status?peers=true` 中查找 `ExitNodeOption` 且在线的对端（目标可为 StableNodeID 或其 Tailscale IP，最终都写 `ExitNodeID`，`ExitNodeIP` 置空），找不到或离线即拒绝；本机正在发布出口节点时也拒绝（tailscaled 不允许同时发布和使用）。关闭出口节点无需查询对端列表，核对本地服务后只发一次角色 PATCH，并保留防火墙偏好。状态字段 `exitNodeOnline` 来自 `status` 的 `ExitNodeStatus`，仅在其 ID 与 prefs 的 `ExitNodeID` 一致时给出。

### IPv6

出口节点默认请求接管 IPv6，保留上游默认行为；实际是否有 IPv6 默认路由仍需核对。WebUI 的“出口节点接管 IPv6”开关默认为开；关闭时 helper 创建 opt-out 文件 `/data/adb/shellscaled/exit-node-ipv6-local`：

- 文件不存在为开，也是默认值：选中的出口节点同时得到 `::/0`，即上游行为。
- 文件存在（内容不限）为关：只有 `0.0.0.0/0` 进入出口节点，IPv6（包括手机自身 IPv6 接口前缀）继续由 Android 原有网络 / VPN 策略处理。用于没有 IPv6 互联网的出口节点：这类节点对 IPv6 目标回 `Destination unreachable: No route`，开着时双栈网络上的 IPv6 连接会失败。
- tailscaled 在每次 `ipnlocal.routerConfigLocked` 时（仅 GOOS=android）`os.Stat` 该文件，不用环境变量，不需重启守护进程；重启后保持，卸载时随 `/data/adb/shellscaled` 删除。安装和升级不会重置已有文件；首次安装默认为开。

关闭时 `routerConfigLocked` 从 `rs.Routes` 去掉出口对端 AllowedIPs 带来的 `::/0`，不补 `::/0`，allow-LAN 关闭时只把 IPv4 的接口前缀装入隧道；`0.0.0.0/0`、Tailnet 与子网 IPv6 路由及上面的策略规则都不变。table 52 中没有 `::/0` 时，IPv6 查找在 13001 查表失败，继续使用 Android 后续网络 / VPN 规则。过滤放在 ipnlocal 而不是 `router_linux.go` 的 `Set()`：routerCfg 不变时 wgengine 不调用 `router.Set`。

切换后 helper 删除或写入该文件，再向 LocalAPI `POST /localapi/v0/debug?action=reconfig`（空 body）。该动作调用 `LocalBackend.authReconfig()`，routerCfg 变化后 wgengine 调用 `router.Set`，由 cidrDiff 在 table 52 增删 `::/0 dev tailscale0`。重复 PATCH 相同的 `ExitNodeID` 无效（prefs 相同时 `editPrefsLocked` 提前返回，routerCfg 相同时 Reconfig 返回 ErrNoChanges），`force-netmap-update` 也不调用 `authReconfig`。未选出口节点时该调用无副作用，文件状态在下次选择时生效。不含该动作的守护进程（CLI android-v1.104.4-r5 及更早）返回 400 `unknown action`。这类守护进程总让 `::/0` 经过出口节点，所以打开开关时 helper 视为成功；关闭时 helper 提示设置已保存、需要随本版模块发布的后台服务才能生效。

状态语义：`exitNodeIPv6Takeover` 表示 opt-out 文件不存在（默认为真）；`exitNodeIPv6` 检查 `/proc/net/ipv6_route` 中是否有经 `tailscale0` 的 `::/0`。开关为开且 `exitNodeIPv6` 为假时提示 IPv6 不经过出口节点；开关为关时说明 IPv6 使用当前网络，此时 `exitNodeIPv6` 仍为真（守护进程不支持该开关）则改为提示 IPv6 仍经过出口节点。

开关为开时，`getV6Available()` 为假仍会使 `::/0` 被静默跳过，IPv6 流量直接出网。条件包括：netfilter runner 创建失败或不支持 IPv6、tailscaled 的 PATH 中没有 `ip6tables`、`disable_ipv6` / `disable_policy` 为 1、IPv6 策略路由不可用、`tailscale0` 禁用 IPv6。守护进程会在启动时记录 `disabling tunneled IPv6 due to ...`。模块只报告不修复（见上面的状态语义）；修复需要改 fork 或全局 sysctl，不在范围内。

### 设备检查

```sh
ip rule; ip -6 rule
ip route show table 52; ip -6 route show table 52
cat /proc/sys/net/ipv4/conf/*/rp_filter
cat /proc/sys/net/ipv6/conf/tailscale0/disable_ipv6
grep 'disabling tunneled IPv6\|allowing exit node access to local IPs' /data/adb/shellscaled/logs/daemon.log
ls -l /data/adb/shellscaled/exit-node-ipv6-local
ip route get 1.1.1.1 uid 10123                 # 结果取决于实际 UID / VPN 规则
```

开启出口节点后分别在 Wi-Fi 和蜂窝下解析公共域名、打开网页、确认 IP 查询站点显示出口节点地址；关闭出口机器后应显示离线提示，“关闭出口节点”一次即可恢复。

## 上游与维护

原模块当前 main 最后提交为 `dffc1aee6fa80dd435a95776a37f979e0204e590`，日期 2025-11-04；其 README 仍描述历史 userspace + hev 方案，而当时代码已采用 Android CLI。因此重构以实际源码为依据。

本次核查官方 [stable 包索引](https://pkgs.tailscale.com/stable/) 和 Tailscale `v1.104.4` release 源码，选择相同版本作为模块版本。源码仓库保留官方 release 的历史，再独立维护 Android 补丁，避免把 Android 改动混入上游提交。没有 clone AOSP；仅下载必要的官方单文件。
