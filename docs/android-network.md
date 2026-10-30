# Android 网络调研与实现边界

## 为什么使用 root TUN

Android 同一用户或工作资料仅允许一个活动 VpnService；第二个 VPN 会替换第一个。[Android VPN 开发文档](https://developer.android.com/develop/connectivity/vpn) 描述了这一生命周期。

Shellscaled 直接通过 root 打开 `/dev/net/tun`，由 tailscaled 管理 `tailscale0`，不会向 Android 注册第二个 VpnService。只避免占用 VPN 槽位还不够：Android 使用 netd 的 UID、网络绑定、fwmark 和策略路由选择网络，必须同时适配这些规则。

## fwmark 与受保护 socket

[AOSP Fwmark.h](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/include/Fwmark.h) 将低 16 位用于 netId，第 16 位用于显式网络选择，第 17 位用于 `protectedFromVpn`，第 18–19 位用于权限，第 20 位用于计费标记。不能直接照搬桌面 Linux 的 Tailscale mark 分配。

Android 移植将自有 mask 放在 `0x1e000000`，tailscaled 外层传输 socket 使用 `SO_MARK=0x10020000`，包含自有 bypass 位和 Android 的受保护位。路由匹配只检查自有 mask 内的 `0x10000000`，保留 netd 的低位语义。原生 VPN App 通常通过 [VpnService.protect](https://developer.android.com/reference/android/net/VpnService#protect(int)) 请求同类保护，root daemon 使用 socket mark 完成。

[Linux fib_rules.c](https://github.com/torvalds/linux/blob/master/net/core/fib_rules.c) 的 mark 匹配对 XOR 结果施加 mask，规则的 `not` 反转整个谓词；因此测试必须同时覆盖普通 socket、携带 netId 的 socket 及 daemon 外层 socket，不能只比较常量。

## Android 策略路由

[RouteController.h](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/server/RouteController.h) 定义了 AOSP 策略优先级；[RouteController.cpp](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/server/RouteController.cpp) 包含 VPN UID 范围与受保护 mark 的匹配逻辑。典型 secure VPN 优先级为 13000，非 VPN 禁止规则为 14000，显式网络为 16000，隐式网络为 23000，默认网络为 31000。设备厂商可以调整实际行为，模拟器须读取真实规则核对。

本移植保留 table 52，在优先级 12500 只匹配 Tailnet 的 `100.64.0.0/10` 和 `fd7a:115c:a1e0::/48`，并排除自有 mark。这样 Tailnet 目标可先于普通 VPN 处理。优先级 13001 的规则为非 bypass 流量查询 table 52，位于 secure VPN 之后；表中没有目标路由时，继续使用 Android 的后续规则。未选择出口节点时，不在该表安装互联网默认路由；选择出口节点后的行为见“出口节点”一节。

这意味着普通 Tailnet 地址和其他子网路由具有不同优先级。显式绑定到网络的 socket、更高优先级的 UID 规则、VPN lockdown 或其他 root 模块可能影响结果；不能据此推断所有 VPN 都兼容。出口节点也需要单独验收，不能从普通 Tailnet 共存测试推广。

原 Android fork 的实现存在 IPv4 / IPv6 规则重复遍历、命令回退遗漏 `Dst` / `Invert`、规则恢复监控仍使用 Linux 优先级范围等问题。当前已统一地址族筛选和命令 / netlink 参数，并按实际 Android 优先级识别删除事件；规则清理仍限定本模块的规则形状和 table，不全局 flush。

模拟器进一步发现上游 netmon 只订阅 IPv4 rule 组，且复用 route 消息解析器解码 FIB rule。Android 的 `FRA_UID_RANGE` 与 route 属性编号冲突，会产生解析错误。当前改为订阅 IPv4 / IPv6 rule 事件，按 `fib_rule_hdr` 和 `FRA_PRIORITY` / `FRA_TABLE` 解码，忽略无关属性，并保留完整 32 位表编号。新增回归测试及真实删除 / 恢复测试已覆盖该问题。

## DNS、防火墙与权限

默认 `accept-dns=false`，DNS 配置器拒绝接管系统 DNS，不使用旧 fork 的全局 UDP 53 DNAT。保留 netd、Private DNS 与已有 VPN 的解析策略。CGO 构建使用 Android bionic 解析，避免依赖不存在的 Linux `/etc/resolv.conf`。因此本期不提供系统 MagicDNS，也不宣称实现 Tailscale 的分域 DNS。

默认 `netfilter-mode=off`，避免向 Android 的防火墙链插入桌面 Linux 规则；唯一例外见“出口节点”一节的 NetfilterMode 规则。普通本机 Tailnet 连通与转发场景分别验收；没有转发验证不能宣称热点、子网路由器或出口节点可用。

[Android SELinux 文档](https://source.android.com/docs/security/features/selinux) 明确指出 root 也受强制访问控制约束。模拟器测试保持 enforcing，并检查 AVC；不能用切换 permissive 代替权限适配。

## 出口节点

### 路由

选择出口节点后，tailscaled 无论 NetfilterMode 取值都会向 table 52 强制写入 `0.0.0.0/0` 和 `::/0`（即使对端未发布出口路由，也宁可黑洞不泄漏）。优先级 13001 的规则（见“Android 策略路由”）因此让所有不带 bypass 位的流量进入隧道，只有 tailscaled 自身带 `0x10020000` 的传输 socket 绕过。

`exit-node-allow-lan-access` 关闭时，手机自身各接口的前缀（Wi-Fi 局域网 /24、蜂窝前缀、全局 IPv6 /64、`fe80::/64`）也作为路由装入 `tailscale0`，局域网网关和局域网 DNS 都经出口节点转发而不可达。开启时这些前缀变为 table 52 中的 throw 路由，查表失败后继续走 Android 后续规则。throw 只覆盖接口前缀：不在本链路前缀内的运营商 DNS 或公共解析器仍经隧道。

### NetfilterMode

出口节点的路由在 off / nodivert / on 三种模式下完全相同，客户端不需要 netfilter。on 唯一与客户端相关的作用是 connmark 回写加 `net.ipv4.conf.all.src_valid_mark=1`：table 52 有默认路由后，严格 rp_filter 以 mark 0 校验 tailscaled 的 WireGuard / DERP 回包，13001 不带源地址条件，会把查找送入 `tailscale0`，回包被当作 martian 丢弃，隧道随之中断。

因此应用出口节点时 helper 读取 `/proc/sys/net/ipv4/conf/*/rp_filter`（跳过 `all`、`default`、`lo`、`tailscale0`），任一接口 `max(all, iface) == 1` 时设置 `NetfilterMode=2`，否则为 0。关闭出口节点总是设置 0。代价（仅在严格 rp_filter 时承担）：

- `ts-input` 的 `! -i tailscale0 -s 100.64.0.0/10 -j DROP` 可能丢弃使用 CGNAT 地址的运营商流量；
- `ts-input` / `ts-forward` 插入 INPUT / FORWARD 第一位；
- 全局写入 `src_valid_mark`；
- 上游 `AddConnmarkSaveRule` 的幂等检查写死 `ctmask 0xff0000`，Android mask 不匹配，反复开启会叠加重复 mangle 规则（删除逻辑正确）。

命令行白名单仍拒绝 `--netfilter-mode=on`，服务脚本首次初始化仍写 off。

### 应用前检查

一个已选但离线、未获批准或未发布出口路由的节点会按设计黑洞所有流量。helper 在 PATCH 前从 `status?peers=true` 中查找 `ExitNodeOption` 且在线的对端（目标可为 StableNodeID 或其 Tailscale IP，最终都写 `ExitNodeID`，`ExitNodeIP` 置空），找不到或离线即拒绝；本机正在发布出口节点时也拒绝（tailscaled 不允许同时发布和使用）。关闭出口节点不做任何查询，只发一次 PATCH。状态字段 `exitNodeOnline` 来自 `status` 的 `ExitNodeStatus`，仅在其 ID 与 prefs 的 `ExitNodeID` 一致时给出。

### 设备检查

```sh
ip rule; ip -6 rule
ip route show table 52; ip -6 route show table 52
cat /proc/sys/net/ipv4/conf/*/rp_filter
grep 'allowing exit node access to local IPs' /data/adb/shellscaled/logs/daemon.log
ip route get 1.1.1.1 uid 10123                 # tailscale0
```

开启出口节点后分别在 Wi-Fi 和蜂窝下解析公共域名、打开网页、确认 IP 查询站点显示出口节点地址；关闭出口机器后应显示离线提示，“关闭出口节点”一次即可恢复。

## 上游与维护

原模块当前 main 最后提交为 `dffc1aee6fa80dd435a95776a37f979e0204e590`，日期 2025-11-04；其 README 仍描述历史 userspace + hev 方案，而当时代码已采用 Android CLI。因此重构以实际源码为依据。

本次核查官方 [stable 包索引](https://pkgs.tailscale.com/stable/) 和 Tailscale `v1.104.4` release 源码，选择相同版本作为模块版本。源码仓库保留官方 release 的历史，再独立维护 Android 补丁，避免把 Android 改动混入上游提交。没有 clone AOSP；仅下载必要的官方单文件。
