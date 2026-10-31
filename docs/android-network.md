# Android 网络调研与实现边界

## 为什么使用 root TUN

Android 同一用户或工作资料仅允许一个活动 VpnService；第二个 VPN 会替换第一个。[Android VPN 开发文档](https://developer.android.com/develop/connectivity/vpn) 描述了这一生命周期。

Shellscaled 直接通过 root 打开 `/dev/net/tun`，由 tailscaled 管理 `tailscale0`，不会向 Android 注册第二个 VpnService。只避免占用 VPN 槽位还不够：Android 使用 netd 的 UID、网络绑定、fwmark 和策略路由选择网络，必须同时适配这些规则。

## fwmark 与受保护 socket

[AOSP Fwmark.h](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/include/Fwmark.h) 将低 16 位用于 netId，第 16 位用于显式网络选择，第 17 位用于 `protectedFromVpn`，第 18–19 位用于权限，第 20 位用于计费标记。不能直接照搬桌面 Linux 的 Tailscale mark 分配。

Android 移植将自有 mask 放在 `0x1e000000`，tailscaled 外层传输 socket 使用 `SO_MARK=0x10020000`，包含自有 bypass 位和 Android 的受保护位。12500 的 Tailnet 规则只检查自有 mask 内的 `0x10000000`；13001 的后备规则另外要求 netd 的显式选择位和 `protectedFromVpn` 位为 0（见“Android 策略路由”），其余低位语义保持不变。原生 VPN App 通常通过 [VpnService.protect](https://developer.android.com/reference/android/net/VpnService#protect(int)) 请求同类保护，root daemon 使用 socket mark 完成。

[Linux fib_rules.c](https://github.com/torvalds/linux/blob/master/net/core/fib_rules.c) 的 mark 匹配对 XOR 结果施加 mask，规则的 `not` 反转整个谓词；因此测试必须同时覆盖普通 socket、携带 netId 的 socket 及 daemon 外层 socket，不能只比较常量。

## Android 策略路由

[RouteController.h](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/server/RouteController.h) 定义了 AOSP 策略优先级；[RouteController.cpp](https://android.googlesource.com/platform/system/netd/+/refs/heads/main/server/RouteController.cpp) 包含 VPN UID 范围与受保护 mark 的匹配逻辑。典型 secure VPN 优先级为 13000，非 VPN 禁止规则为 14000，显式网络为 16000，隐式网络为 23000，默认网络为 31000。设备厂商可以调整实际行为，模拟器须读取真实规则核对。

本移植保留 table 52，在优先级 12500 只匹配 Tailnet 的 `100.64.0.0/10` 和 `fd7a:115c:a1e0::/48`，并排除自有 mark。这样 Tailnet 目标可先于普通 VPN 处理。优先级 13001 的单条规则 `from all fwmark 0x0/0x10030000 lookup 52`（两个地址族）为其余流量查询 table 52，位于 secure VPN 之后；表中没有目标路由时，继续使用 Android 的后续规则。未选择出口节点时，不在该表安装互联网默认路由；选择出口节点后的行为见“出口节点”一节。

下表中“mask X”表示 `fwmark 0x0/X`。`0x10030000` = 显式选择位 `0x10000` | `protectedFromVpn` 位 `0x20000` | 自有 bypass 位 `0x10000000`，即只有三者都未设置的 socket 才会进入后备规则，与原生可绕过（bypassable）VPN 的 UID 规则一致。规则不带源地址条件。

| 优先级 | 地址族 | 规则 | 动作 |
|---|---|---|---|
| 12500 | v4 | `to 100.64.0.0/10`，mask `0x1e000000` | lookup 52 |
| 12500 | v6 | `to fd7a:115c:a1e0::/48`，mask `0x1e000000` | lookup 52 |
| 13001 | v4 / v6 | `from all`，mask `0x10030000` | lookup 52 |

由此得到的行为，与原生 VpnService 一致：
- 出口节点生效时，App 的连接不论新建还是已建立都进入 table 52。已建立连接的源地址仍是 Wi-Fi / 蜂窝地址，进入隧道后会被出口节点丢弃，所以由 MagicDNS 代理立即重置（见“出口节点”一节的“失效连接重置”），App 重连后走新路径。
- 显式绑定网络的 socket（`Network.bindSocket`、NetworkStack 验证探测）和 `protectedFromVpn` 的 socket（netd DNS / DoT、其他 VPN 的传输、clatd）绕过出口节点，与原生 Android 相同。因此 Private DNS 的 DoT 与网络验证改走物理网络。
- tailscaled 与 DNS 代理转发 socket（`0x10020000`）始终绕过。
- Tailnet 目标仍由 12500 接管，包括显式绑定的 socket；只有带自有 bypass 位的 socket 例外。
- 出口节点开启期间建立的连接源地址为 `100.x` / `fd7a:`，关闭出口节点后失效（目标仍在已接受的子网路由内的除外），同样被重置。没有为此加入 `100.64.0.0/10` 源地址的快速失败 unreachable 规则：运营商使用 CGNAT 地址时，它会在未选出口节点时破坏蜂窝网络。

上游 CLI android-v1.104.4-r5 及更早版本的 13001 为 `not fwmark 0x10000000/0x1e000000 lookup 52`（两个地址族），不检查显式 / 受保护位。在该规则下，选中出口节点后 netd 自己的 DNS socket（`protectedFromVpn`，不带 bypass 位）也进入 table 52，局域网与运营商的解析器因此不可达，IP 连通而 DNS 失败。该形状只在删除列表中（`androidLegacyIPRules`，经 `ipRulesToDelete` 用于每次启动和关闭时的 `delIPRules`），从不安装，所以不重启升级或守护进程崩溃后残留的旧规则会被清掉。内核删除规则时比较非零的 mark 和 mask，旧形状（mark `0x10000000`）与当前规则（mark 0、mask `0x10030000`）互不误删；netlink 删除保留每条规则声明的 mark / mask，mask 为 0 表示无谓词。

这意味着普通 Tailnet 地址和其他子网路由具有不同优先级。显式绑定到网络的 socket、更高优先级的 UID 规则、VPN lockdown 或其他 root 模块可能影响结果；不能据此推断所有 VPN 都兼容。出口节点也需要单独验收，不能从普通 Tailnet 共存测试推广。

原 Android fork 的实现存在 IPv4 / IPv6 规则重复遍历、命令回退遗漏 `Dst` / `Invert`、规则恢复监控仍使用 Linux 优先级范围等问题。当前已统一地址族筛选和命令 / netlink 参数，并按实际 Android 优先级识别删除事件；规则清理仍限定本模块的规则形状和 table，不全局 flush。

模拟器进一步发现上游 netmon 只订阅 IPv4 rule 组，且复用 route 消息解析器解码 FIB rule。Android 的 `FRA_UID_RANGE` 与 route 属性编号冲突，会产生解析错误。当前改为订阅 IPv4 / IPv6 rule 事件，按 `fib_rule_hdr` 和 `FRA_PRIORITY` / `FRA_TABLE` 解码，忽略无关属性，并保留完整 32 位表编号。新增回归测试及真实删除 / 恢复测试已覆盖该问题。

## DNS、防火墙与权限

默认 `accept-dns=false`，Tailscale 自身不接管 Android 的 netd 配置，也不修改 Private DNS。模块用一个本地 MagicDNS 代理补上 tailnet 名称解析：普通模式下只有 tailnet 名称的查询被交给代理，其他查询原样发往 Android 选定的 DNS 服务器；需要把全部 DNS 交给代理时，非 tailnet 查询仍转发给系统原本选定的那台服务器。IPv4 与 IPv6 的 DNS 查询走同样的路径。

### MagicDNS 机制

代理不读取 Android 的 DNS 服务器配置：本移植中 LocalAPI `dns-osconfig` 始终返回 500（Android 的 OS DNS 管理器不支持读取基础配置），Android 8 起也没有 `net.dns*` 属性。代理逐个查询取回它被 REDIRECT 之前的目的地址（见下文“原目的地址转发”）。

内核对象全部归模块所有：

- IPv4：`iptables` `nat` 表链 `shellscaled_dns`，由 `OUTPUT -p udp --dport 53 -j shellscaled_dns` 进入。
- IPv6：`ip6tables` `nat` 表链 `shellscaled_dns`，同样的跳转。Android GKI 内核提供 IPv6 NAT：Pixel（KernelSU）上 `/proc/net/ip6_tables_targets` 含 `REDIRECT` / `DNAT` / `TPROXY`，`ip6tables -t nat -L OUTPUT` 可用。没有 IPv6 NAT 的内核见下文“缺少 IPv6 NAT 的内核”。
- 服务脚本创建空链和跳转，停止和卸载时删除；代理只对该链执行 `-F` / `-A`，退出时清空。链初始为空，代理未运行时不会拦截任何 DNS。

代理监听 `127.0.0.1:1053` 和 `[::1]:1053` 两个 socket，处理逻辑相同：nat `OUTPUT` 中的 REDIRECT 把本机发出的包改写到该地址族的回环地址。`[::1]` 无法绑定时记录一行日志，IPv6 链保持为空（IPv6 DNS 留在系统路径），`rules6` 为 false。运行中 `[::1]` 的读取出错退出时同样处理：刷新循环清空 IPv6 链，`rules6` 变为 false，直到代理重启。

IPv6 必须和 IPv4 一样交给代理：双栈网络上 netd 优先使用 IPv6 DNS 服务器。例如双栈蜂窝网络同时下发 IPv6 和 IPv4 DNS 服务器时，tailnet 名称的 A 与 AAAA 查询都发往 IPv6 服务器，只拦截 IPv4 时这类名称无法解析。

**缺少 IPv6 NAT 的内核**

部分内核没有 IPv6 `nat` 表（`ip6tables -t nat -S OUTPUT` 失败），无法把 IPv6 DNS 重定向到 `[::1]:1053`。双栈网络上 netd 优先查询 IPv6 DNS 服务器，tailnet 名称因此超时。

这类内核改用模块自有的 IPv6 `filter` 链 `shellscaled_dns6_fallback`，同样通过一条 UDP/53 OUTPUT 跳转进入，第一条规则同样放行带 bypass 位的查询。被拒绝的查询收到本地 `icmp6-port-unreachable`，Android resolver 随即改问 IPv4 DNS 服务器，再由 IPv4 链交给代理：

- `tailnet`：只拒绝 tailnet 后缀（启用 accept-dns 时也包括 split-DNS 后缀）的查询，普通域名仍走系统路径。tailnet 的 A、AAAA 记录都经 IPv4 入口解析，tailnet IPv6 地址照常可用。
- `exit-doh` / `exit-passthrough`：拒绝全部 IPv6 UDP/53，所有查询经 IPv4 交给代理，与有 IPv6 NAT 时一样不从物理网络直接查询。
- `passthrough-fallback`：内核没有 `xt_string`，后缀规则无法安装，`rules6` 报告 false。

该路径要求网络提供可用的 IPv4 DNS 服务器；只提供 IPv6 DNS 的网络不支持。模块不修改 Android DNS 配置、原生 IPv6 地址和路由。停止、断开和卸载时删除该链，`sc_magicdns_jumps` 按内核能力检查对应的 OUTPUT 跳转，所以热更新同样可以接管代理。状态附加 `ipv6Fallback:true`，WebUI 提示对 IPv4 DNS 的依赖。

代理每 5 秒读取一次 LocalAPI `status` 和 `dns-config`，按模式重建链内容（仅在变化时）。WebUI 应用或关闭出口节点后，helper 向 `magicdns.pid` 中的进程发送 SIGUSR1（先确认 `/proc/<pid>/exe` 与 helper 自身是同一文件，同 `sc_magicdns_running`），代理立即刷新。除 `off` 外，两条链内容基本相同（`xt_string` 偏移不同；整体重定向时 IPv6 链多一条链路本地 RETURN；某一地址族 conntrack 不可用时见下文），第一条总是 `-m mark --mark 0x10000000/0x1e000000 -j RETURN`：代理自己转发的查询带 `SO_MARK=0x10020000`，由此直接返回，不会再次被 REDIRECT；tailscaled 自身带同一 bypass 位的 UDP/53 也不被拦截。第二条 `-j CONNMARK --save-mark --nfmask 0x1ffff --ctmask 0x1ffff` 把 netd 为该查询设置的 netId 与显式选择位存入连接 mark，供原目的地址转发使用。

- `off`：守护进程不可达、未运行，或未选出口节点且没有任何 tailnet 后缀；链为空。
- `tailnet`：对 MagicDNS 后缀，以及 accept-dns 打开时 `dns-config` 的 Routes 后缀（合计最多 16 个；`dns-config` 不论 accept-dns 都返回控制面配置，而 tailscaled 只在 accept-dns 打开时应用 split-DNS，所以默认的 accept-dns=false 下只拦截 MagicDNS 后缀），用 `xt_string` 匹配查询中该后缀的 DNS wire 编码，命中的查询 REDIRECT 到 `127.0.0.1:1053` / `[::1]:1053`。匹配窗口从 QNAME 起点开始：IPv4 为 20（IP）+ 8（UDP）+ 12（DNS 头）= 40，即 `--from 40 --to 300`；IPv6 为 40 + 8 + 12 = 60，即 `--from 60 --to 320`（假设没有扩展头，netd 的 DNS 查询不带扩展头）。窗口长 260 字节，容得下最长 255 字节的 QNAME。其他查询完全不经过代理。
- `exit-doh`：选中出口节点，且对该节点 PeerAPI `/dns-query`（RFC 8484 DoH，Tailscale 自身的出口节点 DNS 路径）的探测返回 200。此时全部 UDP/53（IPv4 和 IPv6）交给代理。进入后每 30 s 复查一次，复查失败即改为 `exit-passthrough`；在此之前单个 DoH 查询失败时，该查询改按原目的地址转发。
- `exit-passthrough`：选中出口节点，但探测失败（Mullvad 等节点、ACL 未授予 `autogroup:internet` DNS、节点版本过旧或没有 PeerAPI）。链内容与 `exit-doh` 相同，tailnet 名称照常解析，其他名称按原目的地址转发；每次刷新（5 s）重新探测，成功即进入 `exit-doh`。该模式不依赖 `xt_string`。
- `passthrough-fallback`：未选出口节点，`tailnet` 模式的 IPv4 规则应用失败，且 `/proc/net/ip_tables_matches` 中没有 `string`（内核没有 `xt_string`）。全部 UDP/53（IPv4 和 IPv6）交给代理，tailnet 名称照常解析，其他名称按原目的地址转发。该判定在代理进程生命周期内保持，不再反复尝试 `xt_string`；其他失败（如 xtables 锁超时）视为暂时性，下次刷新重试。

`exit-passthrough` 和 `passthrough-fallback` 依赖 conntrack 查询。代理在首次需要时分别以 IPv4 和 IPv6 查询一个不存在的连接，返回 ENOENT 的地址族视为可用，两个地址族分别记录。其他错误（SELinux 拒绝、内核无 `CONFIG_NF_CT_NETLINK` 等）记录一行日志，此后该地址族的链不再整体重定向，只装 `tailnet` 的后缀规则（没有 `xt_string` 或没有后缀时为空），另一地址族不受影响；两个地址族都不可用时不再使用这两种模式：有后缀时用 `tailnet`（没有 `xt_string` 时为 `off`），否则为 `off`。

整体重定向时 IPv6 链在 REDIRECT 之前有一条 `-d fe80::/10 -j RETURN`：conntrack 不含接口作用域，代理无法转发到链路本地服务器（部分路由器经 RA 下发），这类查询留在系统路径。`exit-doh` 下这些查询因此不走 DoH。

代理应答规则：

- MagicDNS 后缀和无解析器的 Routes 后缀：调用 LocalAPI `dns-query`（与 `100.100.100.100` 同一个进程内解析器，不依赖路由）。MagicDNS 后缀下的 SERVFAIL / REFUSED 改写为 NXDOMAIN；A、AAAA、CNAME、MX、NS、PTR、SOA、SRV、TXT 以外的类型直接回 NODATA。
- 带解析器的 Routes 后缀：按最长后缀匹配，通过 TCP/53 转发给这些解析器。TCP 不会被 UDP 规则再次拦截，所以没有回环；套接字不加 SO_MARK，`100.x` 解析器经 table 52 进入隧道。
- `exit-doh` 模式下的其他名称：POST 到出口节点 DoH，失败时按原目的地址转发。
- 其他模式下的其他名称（`exit-passthrough`、`passthrough-fallback`，以及 `tailnet` 模式下偶然命中规则的非 tailnet 查询）：按原目的地址转发。
- 任何失败都回 SERVFAIL，非标准查询回 FORMERR；仅在 64 个并发槽占满时丢包（netd 会重试）。超过 512 字节且查询不含 EDNS 的应答截断并置 TC。

### 原目的地址转发

REDIRECT 改写了包头，`IP_RECVORIGDSTADDR` / `IP_ORIGDSTADDR` 报告的是改写后的 `127.0.0.1:1053`（它为 TPROXY 设计），UDP 也没有 `SO_ORIGINAL_DST`。代理因此用 conntrack netlink（`NETLINK_NETFILTER`，`IPCTNL_MSG_CT_GET`）按回复方向元组 `127.0.0.1:1053 → 客户端地址:端口` 查询连接（IPv6 监听上收到的查询用 `nfgenmsg` 族 `AF_INET6`、`CTA_IP_V6_SRC` / `CTA_IP_V6_DST` 和 `[::1]:1053`），取原方向元组的目的地址（`CTA_IP_V4_DST` 或 `CTA_IP_V6_DST`）和端口，以及连接 mark（`CTA_MARK`）。查不到或结果仍是回环地址的 1053 端口时回 SERVFAIL。

转发使用与原目的地址同族的新 UDP socket（`udp4` / `udp6`），`SO_MARK=0x10020000 | (连接 mark & 0x1ffff)`（`SOL_SOCKET` 选项，与地址族无关）：bypass 位与受保护位同 tailscaled 外层 socket，低 17 位是链中 CONNMARK 保存的 netd netId 与显式选择位。带 bypass 位的包跳过 12500 / 13001 与 table 52，再由 Android 显式网络规则（16000）按 netd 为该查询选择的网络出网（例如正在验证的 Wi-Fi），没有 netId 时按默认网络；源地址在 connect 时按该路由选择；两族 nat OUTPUT 中链首的 mark RETURN 规则使它不再进入代理。netd 原本的 DNS socket 带显式选择位和 `protectedFromVpn` 位，不被 13001 捕获（见“Android 策略路由”）；REDIRECT 在 nat `OUTPUT` 完成，与路由规则无关，所以代理仍能收到这些查询。

出口节点下没有 DoH 时，普通 DNS 因此从物理接口直接发往局域网或运营商解析器，不经出口节点。这是有意的：官方 Tailscale Android App 在同样情况下由 tailscaled 经 `VpnService.protect` 保护的 socket 转发到系统解析器，行为等价。代价是这些 DNS 查询对本地网络可见，解析结果按本地网络而非出口节点所在地给出。

### 状态文件与状态接口

代理每次刷新后写 `/data/adb/shellscaled/run/magicdns.json`（0600，先写 `.new` 再改名）：

```json
{"updated":<unix 秒>,"mode":"off|tailnet|exit-doh|exit-passthrough|passthrough-fallback","rules":true,"rules6":true,"privateDns":"off|opportunistic|hostname|unknown","upstream":"system|exit-node|passthrough","daemon":"<BackendState>","exitNode":"<prefs.ExitNodeID>"}
```

`upstream` 为 `system`（`off` / `tailnet`，普通名称不经代理）、`exit-node`（`exit-doh`）或 `passthrough`（`exit-passthrough` / `passthrough-fallback`，按原目的地址转发）。`rules` / `rules6` 表示 IPv4 / IPv6 nat 链内容是否按期望应用（`rules6` 在 `[::1]:1053` 无法监听或监听已停止时为 false；任一为 false 且模式不是 `off` 时，WebUI 显示 MagicDNS 规则未生效）；`-F` 或任一 `-A` 失败时该链被清空，DNS 留在系统路径而不会被黑洞，错误按不同内容各记一行到 `daemon.log`，每次刷新重试直到成功。`privateDns` 每 30 秒用只读的 `cmd settings get global private_dns_mode` 读取。`daemon` / `exitNode` 记录本次决定所依据的 BackendState（LocalAPI 失败时为空）和 prefs 中的出口节点 ID（未选择或未 Running 时为空）；除此之外文件不含后缀、查询名、PeerAPI 地址或解析器地址。RPC `status` 响应的 `magicDNS` 字段转发这些值，并以 `updated` 在 20 秒内作为 `running`；`running` 时若 `daemon` 与当前 BackendState 不同，或 Running 时 `exitNode` 与当前 prefs 不同，则附加 `pending: true`，WebUI 显示“MagicDNS 正在更新…”并在 2 秒后重新拉取状态，而不是显示旧模式。连接 / 断开和切换出口节点后 helper 都会用 SIGUSR1 让代理立即刷新。

### 限制

- Private DNS 为指定主机名，或自动模式下 DoT 验证成功时，netd 使用 tcp/853，不产生 UDP/53，tailnet 名称无法解析。模块不修改 Private DNS，也不阻断 DoT。
- 自带 DoH 的应用（如 Chrome 安全 DNS）绕过拦截。
- 不解析短名称（只解析完整 FQDN）。
- 不拦截 TCP/53 查询。
- IPv6 nat 链应用失败（`rules6` 为 false，例如内核缺少 IPv6 NAT）时，IPv6 DNS 留在系统路径：tailnet 名称在 IPv6 优先的网络上失败；出口节点下 IPv6 DNS 进入隧道，通常不可达。
- 发往 IPv6 链路本地 DNS 服务器（`fe80::/10`）的 tailnet 名称查询只在 `tailnet` 模式下被拦截；整体重定向的模式中这类查询留在系统路径。
- 系统解析器若本身是 `100.x` 地址，带 bypass mark 的转发无法进入隧道。其他 VPN App 的解析器经显式规则路由，是否可达取决于该 VPN 对 root 与受保护 socket 的 UID 规则。
- WebUI 之外（命令行、其他客户端）改变出口节点时，在下一次刷新（5 秒内）生效。
- `xt_string`、`xt_mark`、conntrack netlink 查询（SELinux 与内核 `CONFIG_NF_CT_NETLINK`，IPv4 与 IPv6）、IPv4 / IPv6 REDIRECT 回包路径（`[::1]` 应答经 conntrack 还原为原服务器地址）和各厂商 `nat OUTPUT` 中先于本模块跳转的终止规则都需要在设备上确认。

### 设备验收清单

- `iptables -t nat -S shellscaled_dns` 和 `ip6tables -t nat -S shellscaled_dns`
- `ip6tables -t nat -L shellscaled_dns -n -v` 在 IPv6 优先的网络上解析 tailnet 名称后，后缀规则的计数增加
- `ss -ulpn | grep 1053`（或 `netstat -ulpn`）同时有 `127.0.0.1:1053` 和 `[::1]:1053`
- `cat /data/adb/shellscaled/run/magicdns.json`
- `cmd settings get global private_dns_mode`
- `ping -c1 <host>.<suffix>`
- 结束代理进程后普通网页仍可访问
- 选择出口节点后普通网页可访问；分别用支持和不支持 DoH 的出口节点检查 `mode` 为 `exit-doh` / `exit-passthrough`
- `exit-passthrough` 下 `daemon.log` 没有 conntrack 错误，`ping` 公共域名成功；在 IPv6 优先 DNS 的双栈蜂窝网络下重复一次

默认 `netfilter-mode=off`，避免向 Android 的防火墙链插入桌面 Linux 规则；唯一例外见“出口节点”一节的 NetfilterMode 规则。普通本机 Tailnet 连通与转发场景分别验收；没有转发验证不能宣称热点、子网路由器或出口节点可用。

[Android SELinux 文档](https://source.android.com/docs/security/features/selinux) 明确指出 root 也受强制访问控制约束。模拟器测试保持 enforcing，并检查 AVC；不能用切换 permissive 代替权限适配。

## 出口节点

### 路由

选择出口节点后，tailscaled 无论 NetfilterMode 取值都会向 table 52 强制写入 `0.0.0.0/0` 和 `::/0`（即使对端未发布出口路由，也宁可黑洞不泄漏）。`::/0` 是否写入由下文“IPv6”一节的开关决定（默认写入）。优先级 13001 的规则（见“Android 策略路由”）因此让全部不带显式 / 受保护 / bypass 位的流量进入隧道；显式绑定网络的 socket、`protectedFromVpn` 的 socket（含 netd 自己的 DNS socket）和 tailscaled 自身带 `0x10020000` 的传输 socket 都绕过。开启前已建立的连接由下文“失效连接重置”立即断开。

已建立连接改路由到 `tailscale0` 后源地址仍是 Wi-Fi / 蜂窝地址，出口节点以 `disallowed source` 丢弃，长连接会挂起直到超时，所以这类连接被立即重置，App 随即重连。

`exit-node-allow-lan-access` 关闭时，手机自身各接口的前缀（Wi-Fi 局域网 /24、蜂窝前缀、全局 IPv6 /64、`fe80::/64`）也作为路由装入 `tailscale0`，局域网网关和局域网 DNS 都经出口节点转发而不可达。开启时这些前缀变为 table 52 中的 throw 路由，查表失败后继续走 Android 后续规则。throw 只覆盖接口前缀：不在本链路前缀内的运营商 DNS 或公共解析器仍经隧道。

### 失效连接重置

Android 在 VPN 的 UID 范围变化时（`ConnectivityService.updateVpnUidRanges` → `maybeCloseSockets`）调用 netd `destroyLiveTcpSockets`，对受影响 UID 的 TCP socket 发送 `SOCK_DESTROY`，只豁免 VPN App 自身的 UID。官方 Tailscale App 切换出口节点只是原地更新路由，已有连接被黑洞直到超时。

本模块在 MagicDNS 代理进程中做同样的重置。每次刷新（5 秒，以及连接 / 断开、切换出口节点或 IPv6 开关后的 SIGUSR1）记录路由状态：出口节点 = BackendState 为 Running 时 prefs 的 `ExitNodeID`；exit6 = 选择了出口节点且 IPv6 opt-out 文件不存在；allowLAN = 选择了出口节点且 `ExitNodeAllowLANAccess`。LocalAPI `status` 或 `prefs` 读取失败时状态未知，不记录。状态与上次不同时（代理启动后的首次观测也算），代理用 `NETLINK_SOCK_DIAG` 列出 IPv4 / IPv6 中除 LISTEN、CLOSE、TIME_WAIT 外的 TCP socket 及其 `INET_DIAG_MARK`，对每个不豁免的 socket 用 `RTM_GETROUTE`（`RTM_F_FIB_MATCH`，带 socket 的源 / 目标地址、mark、UID 和绑定接口）向内核查询它的包现在命中的路由，对必然失效的连接发送 `SOCK_DESTROY`（socket id 原样取自内核的返回，同 Android `InetDiagMessage`），重置数大于 0 或出错时向 `daemon.log` 记一行 `exit node routing changed: reset N stale TCP connections`。判定交给内核的当前路由，所以 Android 在 13001 之前的规则（其他 VPN 的 UID 范围、本机地址的 local 表）、子网路由、allow-LAN 的 throw 路由和 IPv6 开关都自动计入；工作中的连接不受影响，重复执行没有副作用：

- 豁免：mark 含 `0x10030000` 任一位（daemon / 代理 socket、显式绑定、`protectedFromVpn`）；内核未返回 mark 的 socket；路由查询失败的 socket。
- Tailnet 源：源地址是 `tailscale0` 上的地址；运营商 CGNAT 的 `100.x` rmnet 地址因此按物理源处理。v4-mapped 地址按 IPv4 处理。
- 命中 local 表（目标为本机接口地址）的连接保留；其余连接只有在“经 `tailscale0` 出去”与“Tailnet 源”一致时可用，不一致即重置：出口节点开启后的物理源连接、关闭后或 allow-LAN 开启后改走物理接口的 Tailnet 源连接。
- 从一个出口节点切换到另一个时，经出口默认路由（前缀长度 0）进入 `tailscale0` 的 Tailnet 源连接也被重置：新出口节点没有它们的 NAT 状态。子网路由和 Tailnet 目标的连接不受影响。

与 Android 相同只处理 TCP；UDP 流不受影响。需要内核 `CONFIG_INET_DIAG_DESTROY`、`CAP_NET_ADMIN`（代理以 root 运行）以及 SELinux 允许 sock_diag 写操作；不满足时只记一行错误，路由行为不变。

### DNS

`accept-dns=false` 时 quad-100 解析器只回答 MagicDNS 节点名，不向任何上游转发（`dnsConfigForNetmap` 在 CorpDNS 关闭时提前返回），因此出口节点下的普通 DNS 无法由 tailscaled 提供。netd 自己的 DNS socket 带 `protectedFromVpn` 位，不被 13001 捕获；出口节点下由下述代理整体接管 UDP/53。

出口节点下的 DNS 由 MagicDNS 代理处理：出口节点的 PeerAPI DoH 探测成功时进入 `exit-doh`，普通名称经出口节点解析；探测失败时进入 `exit-passthrough`，普通名称由代理带 `SO_MARK=0x10020000` 转发给系统原本选定的解析器，绕过 12500/13001 与 table 52，与 allow-LAN 设置无关（见上文 MagicDNS 机制与原目的地址转发）。tailnet 名称始终走 LocalAPI `dns-query`，不受 mark 影响。

模块判定“出口节点生效”的条件与状态字段 `exitNodeConfigured` 相同：prefs 中 `ExitNodeID != ""`，或 `ExitNodeIP` 非空且不等于 `invalid IP`。WebUI 始终提交 StableNodeID，因此 `ExitNodeStatus.ID` 可用于判定。

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

### IPv6

出口节点默认同时接管 IPv6，与上游和官方 Tailscale Android App 一致。WebUI 的“出口节点接管 IPv6”开关默认为开；关闭时 helper 创建 opt-out 文件 `/data/adb/shellscaled/exit-node-ipv6-local`：

- 文件不存在为开，也是默认值：选中的出口节点同时得到 `::/0`，即上游行为。
- 文件存在（内容不限）为关：只有 `0.0.0.0/0` 进入出口节点，IPv6（包括手机自身 IPv6 接口前缀）留在物理网络。用于没有 IPv6 互联网的出口节点：这类节点对 IPv6 目标回 `Destination unreachable: No route`，开着时双栈网络上的 IPv6 连接会失败。
- tailscaled 在每次 `ipnlocal.routerConfigLocked` 时（仅 GOOS=android）`os.Stat` 该文件，不用环境变量，不需重启守护进程；重启后保持，卸载时随 `/data/adb/shellscaled` 删除。安装和升级不创建该文件，开关保持为开。

关闭时 `routerConfigLocked` 从 `rs.Routes` 去掉出口对端 AllowedIPs 带来的 `::/0`，不补 `::/0`，allow-LAN 关闭时只把 IPv4 的接口前缀装入隧道；`0.0.0.0/0`、Tailnet 与子网 IPv6 路由及上面的策略规则都不变。table 52 中没有 `::/0` 时，IPv6 查找在 13001 查表失败，继续使用 Android 的 wwan / wlan 规则并选物理源地址。过滤放在 ipnlocal 而不是 `router_linux.go` 的 `Set()`：routerCfg 不变时 wgengine 不调用 `router.Set`。

切换后 helper 删除或写入该文件，再向 LocalAPI `POST /localapi/v0/debug?action=reconfig`（空 body）。该动作调用 `LocalBackend.authReconfig()`，routerCfg 变化后 wgengine 调用 `router.Set`，由 cidrDiff 在 table 52 增删 `::/0 dev tailscale0`。重复 PATCH 相同的 `ExitNodeID` 无效（prefs 相同时 `editPrefsLocked` 提前返回，routerCfg 相同时 Reconfig 返回 ErrNoChanges），`force-netmap-update` 也不调用 `authReconfig`。未选出口节点时该调用无副作用，文件状态在下次选择时生效。不含该动作的守护进程（CLI android-v1.104.4-r5 及更早）返回 400 `unknown action`。这类守护进程总让 `::/0` 经过出口节点，所以打开开关时 helper 视为成功；关闭时 helper 提示设置已保存、需要随本版模块发布的后台服务才能生效。

状态语义：`exitNodeIPv6Takeover` 表示 opt-out 文件不存在（默认为真）；`exitNodeIPv6` 检查 `/proc/net/ipv6_route` 中是否有经 `tailscale0` 的 `::/0`。开关为开且 `exitNodeIPv6` 为假时提示 IPv6 不经过出口节点；开关为关时说明 IPv6 使用物理网络，此时 `exitNodeIPv6` 仍为真（守护进程不支持该开关）则改为提示 IPv6 仍经过出口节点。

开关为开时，`getV6Available()` 为假仍会使 `::/0` 被静默跳过，IPv6 流量直接出网。条件包括：netfilter runner 创建失败或不支持 IPv6、tailscaled 的 PATH 中没有 `ip6tables`、`disable_ipv6` / `disable_policy` 为 1、IPv6 策略路由不可用、`tailscale0` 禁用 IPv6。守护进程会在启动时记录 `disabling tunneled IPv6 due to ...`。模块只报告不修复（见上面的状态语义）；修复需要改 fork 或全局 sysctl，不在范围内。

### 设备检查

```sh
ip rule; ip -6 rule
ip route show table 52; ip -6 route show table 52
cat /proc/sys/net/ipv4/conf/*/rp_filter
cat /proc/sys/net/ipv6/conf/tailscale0/disable_ipv6
grep 'disabling tunneled IPv6\|allowing exit node access to local IPs\|exit node routing changed' /data/adb/shellscaled/logs/daemon.log
ls -l /data/adb/shellscaled/exit-node-ipv6-local
ip route get 1.1.1.1 uid 10123                 # tailscale0
ss -tn state established | wc -l               # 切换出口节点前后对比
```

期望：`ip rule` 与 `ip -6 rule` 各有 13001 `from all fwmark 0/0x10030000 lookup 52`；没有 `not fwmark 0x10000000/0x1e000000`（包括从 CLI android-v1.104.4-r5 不重启升级之后）。IPv6 开关为关时 `ip -6 route show table 52` 没有 default。切换出口节点后 `daemon.log` 有一行 `exit node routing changed: reset N stale TCP connections`（有可重置的连接时），且没有该行的失败形式。

开启出口节点后分别在 Wi-Fi 和蜂窝下解析公共域名、打开网页、确认 IP 查询站点显示出口节点地址、解析 MagicDNS 名称；关闭出口机器后应显示离线提示，“关闭出口节点”一次即可恢复。

### 未实现与不支持

- 失效连接重置只处理本机 TCP socket；UDP 流和热点 / 转发流量不处理。转发包在出口节点开启时是否被 13001 捕获取决于其 fwmark，未经设备验收。

## 上游与维护

原模块当前 main 最后提交为 `dffc1aee6fa80dd435a95776a37f979e0204e590`，日期 2025-11-04；其 README 仍描述历史 userspace + hev 方案，而当时代码已采用 Android CLI。因此重构以实际源码为依据。

本次核查官方 [stable 包索引](https://pkgs.tailscale.com/stable/) 和 Tailscale `v1.104.4` release 源码，选择相同版本作为模块版本。源码仓库保留官方 release 的历史，再独立维护 Android 补丁，避免把 Android 改动混入上游提交。没有 clone AOSP；仅下载必要的官方单文件。
