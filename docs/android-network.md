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

本移植保留 table 52，在优先级 12500 只匹配 Tailnet 的 `100.64.0.0/10` 和 `fd7a:115c:a1e0::/48`，并排除自有 mark。这样 Tailnet 目标可先于普通 VPN 处理。优先级 13001 的规则为非 bypass 流量查询 table 52，位于 secure VPN 之后；表中没有目标路由时，继续使用 Android 的后续规则。默认不在该表安装互联网默认路由。

这意味着普通 Tailnet 地址和其他子网路由具有不同优先级。显式绑定到网络的 socket、更高优先级的 UID 规则、VPN lockdown 或其他 root 模块可能影响结果；不能据此推断所有 VPN 都兼容。出口节点也需要单独验收，不能从普通 Tailnet 共存测试推广。

原 Android fork 的实现存在 IPv4 / IPv6 规则重复遍历、命令回退遗漏 `Dst` / `Invert`、规则恢复监控仍使用 Linux 优先级范围等问题。当前已统一地址族筛选和命令 / netlink 参数，并按实际 Android 优先级识别删除事件；规则清理仍限定本模块的规则形状和 table，不全局 flush。

模拟器进一步发现上游 netmon 只订阅 IPv4 rule 组，且复用 route 消息解析器解码 FIB rule。Android 的 `FRA_UID_RANGE` 与 route 属性编号冲突，会产生解析错误。当前改为订阅 IPv4 / IPv6 rule 事件，按 `fib_rule_hdr` 和 `FRA_PRIORITY` / `FRA_TABLE` 解码，忽略无关属性，并保留完整 32 位表编号。新增回归测试及真实删除 / 恢复测试已覆盖该问题。

## DNS、防火墙与权限

默认 `accept-dns=false`，DNS 配置器拒绝接管系统 DNS，不使用旧 fork 的全局 UDP 53 DNAT。保留 netd、Private DNS 与已有 VPN 的解析策略。CGO 构建使用 Android bionic 解析，避免依赖不存在的 Linux `/etc/resolv.conf`。因此本期不提供系统 MagicDNS，也不宣称实现 Tailscale 的分域 DNS。

默认 `netfilter-mode=off`，避免向 Android 的防火墙链插入桌面 Linux 规则。普通本机 Tailnet 连通与转发场景分别验收；没有转发验证不能宣称热点、子网路由器或出口节点可用。

[Android SELinux 文档](https://source.android.com/docs/security/features/selinux) 明确指出 root 也受强制访问控制约束。模拟器测试保持 enforcing，并检查 AVC；不能用切换 permissive 代替权限适配。

## 上游与维护

原模块当前 main 最后提交为 `dffc1aee6fa80dd435a95776a37f979e0204e590`，日期 2025-11-04；其 README 仍描述历史 userspace + hev 方案，而当时代码已采用 Android CLI。因此重构以实际源码为依据。

本次核查官方 [stable 包索引](https://pkgs.tailscale.com/stable/) 和 Tailscale `v1.104.2` 源码，选择相同版本作为模块版本。源码私有仓库使用带上游版本 / 提交说明的源码快照，再独立维护 Android 补丁，避免在本机拉取完整上游历史。没有 clone AOSP；仅下载必要的官方单文件。
