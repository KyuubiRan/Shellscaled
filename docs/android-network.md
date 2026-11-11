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

本移植保留 table 52，在优先级 12500 匹配 Tailnet 的 `100.64.0.0/10` 和 `fd7a:115c:a1e0::/48`，并排除自有 bypass mark。该规则早于 Android 17 secure VPN 的基础优先级。13001 后备规则只匹配 `fwmark 0x0/0x10030000`；它早于 bypassable VPN 基础优先级，且不能笼统认为晚于所有带 subpriority 的 secure VPN 规则。查表没有目标路由时继续使用 Android 后续规则；未选择出口节点时，该表没有互联网默认路由。出口节点开启或关闭时，已建立连接的处理见“出口节点”一节的“失效连接重置”。

这意味着普通 Tailnet 地址和其他子网路由具有不同优先级。显式绑定到网络的 socket、更高优先级的 UID 规则、VPN lockdown 或其他 root 模块可能影响结果；不能据此推断所有 VPN 都兼容。出口节点也需要单独验收，不能从普通 Tailnet 共存测试推广。

原 Android fork 的实现存在 IPv4 / IPv6 规则重复遍历、命令回退遗漏 `Dst` / `Invert`、规则恢复监控仍使用 Linux 优先级范围等问题。当前已统一地址族筛选和命令 / netlink 参数，并按实际 Android 优先级识别删除事件；规则清理仍限定本模块的规则形状和 table，不全局 flush。

模拟器进一步发现上游 netmon 只订阅 IPv4 rule 组，且复用 route 消息解析器解码 FIB rule。Android 的 `FRA_UID_RANGE` 与 route 属性编号冲突，会产生解析错误。当前改为订阅 IPv4 / IPv6 rule 事件，按 `fib_rule_hdr` 和 `FRA_PRIORITY` / `FRA_TABLE` 解码，忽略无关属性，并保留完整 32 位表编号。新增回归测试及真实删除 / 恢复测试已覆盖该问题。

## DNS、防火墙与权限

默认 `accept-dns=false`，Tailscale 自身不接管 Android 的 netd 配置，也不修改 Private DNS。模块可选用一个本地 MagicDNS 代理补上 tailnet 名称解析。该功能默认关闭，在 WebUI“网络角色”卡片中打开（RPC `magicdns`，请求文件 `/data/adb/shellscaled/magicdns-enabled`）；关闭时代理进程仍运行（它同时负责下文的失效连接重置），收到请求后清空两条链，不再拦截 DNS。界面在代理确认完成前显示正在应用，失败时保留提示并重试。开启后：普通模式下只有 tailnet 名称的查询被交给代理，其他查询原样发往 Android 选定的 DNS 服务器；需要把全部 DNS 交给代理时，非 tailnet 查询仍转发给系统原本选定的那台服务器。IPv4 与 IPv6 的 DNS 查询走同样的路径。

请求文件使用原子 JSON 记录 `enabled` 和随机 `generation`，包括关闭请求；不存在时默认为关闭，旧的空文件或 `1` 标记仍解释为开启。代理状态中的 `enabled` 和内部代次仅在所有受控地址族的规则操作成功后确认，区别于 status 顶层表示用户选择的 `magicDNSEnabled`。未确认的代次标为 `pending`，清理失败时增加 `applyFailed` 并重试；界面即使开关已经关闭也显示这些状态。RPC 写请求与代理应用规则共用专用 flock，获取最多等待 2 秒并响应 context 取消。LocalAPI / DoH 探测不持锁，代理应用前在锁内重新核对代次，过期计划不会重新安装规则；只有代理操作链内容，RPC 不另行执行 iptables。

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

该路径要求网络提供可用的 IPv4 DNS 服务器；只提供 IPv6 DNS 的网络不支持。模块不修改 Android DNS 配置、原生 IPv6 地址和路由。停止、断开和卸载时删除该链。状态附加 `ipv6Fallback:true`，WebUI 提示对 IPv4 DNS 的依赖。

代理每 5 秒读取一次 LocalAPI `status` 和 `dns-config`，按模式重建链内容（仅在变化时）。WebUI 应用或关闭出口节点后，helper 向 `magicdns.pid` 中的进程发送 SIGUSR1（先确认 `/proc/<pid>/exe` 与 helper 自身是同一文件，同 `sc_magicdns_running`），代理立即刷新。除 `off` 外，两条链内容基本相同（`xt_string` 偏移不同；整体重定向时 IPv6 链多一条链路本地 RETURN；某一地址族 conntrack 不可用时见下文），第一条总是 `-m mark --mark 0x10000000/0x1e000000 -j RETURN`：代理自己转发的查询带 `SO_MARK=0x10020000`，由此直接返回，不会再次被 REDIRECT；tailscaled 自身带同一 bypass 位的 UDP/53 也不被拦截。第二条 `-j CONNMARK --save-mark --nfmask 0x1ffff --ctmask 0x1ffff` 把 netd 为该查询设置的 netId 与显式选择位存入连接 mark，供原目的地址转发使用。

- `off`：MagicDNS 未开启、守护进程不可达或未运行、未选出口节点且没有任何 tailnet 后缀，或未选出口节点且内核没有 `xt_string`；链为空。
- `tailnet`：对 MagicDNS 后缀，以及 accept-dns 打开时 `dns-config` 的 Routes 后缀（合计最多 16 个；`dns-config` 不论 accept-dns 都返回控制面配置，而 tailscaled 只在 accept-dns 打开时应用 split-DNS，所以默认的 accept-dns=false 下只拦截 MagicDNS 后缀），用 `xt_string` 匹配查询中该后缀的 DNS wire 编码，命中的查询 REDIRECT 到 `127.0.0.1:1053` / `[::1]:1053`。匹配窗口从 QNAME 起点开始：IPv4 为 20（IP）+ 8（UDP）+ 12（DNS 头）= 40，即 `--from 40 --to 300`；IPv6 为 40 + 8 + 12 = 60，即 `--from 60 --to 320`（假设没有扩展头，netd 的 DNS 查询不带扩展头）。窗口长 260 字节，容得下最长 255 字节的 QNAME。其他查询完全不经过代理。
- `exit-doh`：选中出口节点，且对该节点 PeerAPI `/dns-query`（RFC 8484 DoH，Tailscale 自身的出口节点 DNS 路径）的探测返回 200。此时全部 UDP/53（IPv4 和 IPv6）交给代理。进入后每 30 s 复查一次，复查失败即改为 `exit-passthrough`；在此之前单个 DoH 查询失败时，该查询改按原目的地址转发。
- `exit-passthrough`：选中出口节点，但探测失败（Mullvad 等节点、ACL 未授予 `autogroup:internet` DNS、节点版本过旧或没有 PeerAPI）。链内容与 `exit-doh` 相同，tailnet 名称照常解析，其他名称按原目的地址转发；每次刷新（5 s）重新探测，成功即进入 `exit-doh`。该模式不依赖 `xt_string`。

`tailnet` 模式的 IPv4 规则应用失败，且 `/proc/net/ip_tables_matches` 中没有 `string`（内核没有 `xt_string`）时，未选出口节点的情况下模式为 `off`，不会改为整体接管 UDP/53；状态附加 `noString: true`，WebUI 提示内核缺少 `xt_string`。该判定在代理进程生命周期内保持，不再反复尝试 `xt_string`；其他失败（如 xtables 锁超时）视为暂时性，下次刷新重试。选中出口节点时仍可进入 `exit-doh` / `exit-passthrough`（这两种模式不依赖 `xt_string`）。

`exit-passthrough` 依赖 conntrack 查询。代理在首次需要时分别以 IPv4 和 IPv6 查询一个不存在的连接，返回 ENOENT 的地址族视为可用，两个地址族分别记录。其他错误（SELinux 拒绝、内核无 `CONFIG_NF_CT_NETLINK` 等）记录一行日志，此后该地址族的链不再整体重定向，只装 `tailnet` 的后缀规则（没有 `xt_string` 或没有后缀时为空），另一地址族不受影响；两个地址族都不可用时不再使用该模式：有后缀时用 `tailnet`（没有 `xt_string` 时为 `off`），否则为 `off`。

整体重定向时 IPv6 链在 REDIRECT 之前有一条 `-d fe80::/10 -j RETURN`：conntrack 不含接口作用域，代理无法转发到链路本地服务器（部分路由器经 RA 下发），这类查询留在系统路径。`exit-doh` 下这些查询因此不走 DoH。

代理应答规则：

- MagicDNS 后缀和无解析器的 Routes 后缀：调用 LocalAPI `dns-query`（与 `100.100.100.100` 同一个进程内解析器，不依赖路由）。MagicDNS 后缀下的 SERVFAIL / REFUSED 改写为 NXDOMAIN；A、AAAA、CNAME、MX、NS、PTR、SOA、SRV、TXT 以外的类型直接回 NODATA。
- 带解析器的 Routes 后缀：按最长后缀匹配，通过 TCP/53 转发给这些解析器。TCP 不会被 UDP 规则再次拦截，所以没有回环；套接字不加 SO_MARK，`100.x` 解析器经 table 52 进入隧道。
- `exit-doh` 模式下的其他名称：POST 到出口节点 DoH，失败时按原目的地址转发。
- 其他模式下的其他名称（`exit-passthrough`，以及 `tailnet` 模式下偶然命中规则的非 tailnet 查询）：按原目的地址转发。
- 任何失败都回 SERVFAIL，非标准查询回 FORMERR；仅在 64 个并发槽占满时丢包（netd 会重试）。超过 512 字节且查询不含 EDNS 的应答截断并置 TC。

### 原目的地址转发

REDIRECT 改写了包头，`IP_RECVORIGDSTADDR` / `IP_ORIGDSTADDR` 报告的是改写后的 `127.0.0.1:1053`（它为 TPROXY 设计），UDP 也没有 `SO_ORIGINAL_DST`。代理因此用 conntrack netlink（`NETLINK_NETFILTER`，`IPCTNL_MSG_CT_GET`）按回复方向元组 `127.0.0.1:1053 → 客户端地址:端口` 查询连接（IPv6 监听上收到的查询用 `nfgenmsg` 族 `AF_INET6`、`CTA_IP_V6_SRC` / `CTA_IP_V6_DST` 和 `[::1]:1053`），取原方向元组的目的地址（`CTA_IP_V4_DST` 或 `CTA_IP_V6_DST`）和端口，以及连接 mark（`CTA_MARK`）。查不到或结果仍是回环地址的 1053 端口时回 SERVFAIL。

转发使用与原目的地址同族的新 UDP socket（`udp4` / `udp6`），`SO_MARK=0x10020000 | (连接 mark & 0x1ffff)`（`SOL_SOCKET` 选项，与地址族无关）：bypass 位与受保护位同 tailscaled 外层 socket，低 17 位是链中 CONNMARK 保存的 netd netId 与显式选择位。带 bypass 位的包跳过 12500 / 13001 与 table 52，再由 Android 显式网络规则（16000）按 netd 为该查询选择的网络出网（例如正在验证的 Wi-Fi），没有 netId 时按默认网络；源地址在 connect 时按该路由选择；两族 nat OUTPUT 中链首的 mark RETURN 规则使它不再进入代理。netd 原本的 DNS socket 带显式选择位和 `protectedFromVpn` 位，不被 13001 捕获（见“Android 策略路由”）；REDIRECT 在 nat `OUTPUT` 完成，与路由规则无关，所以代理仍能收到这些查询。

出口节点下没有 DoH 时，普通 DNS 因此从物理接口直接发往局域网或运营商解析器，不经出口节点。这是有意的：官方 Tailscale Android App 在同样情况下由 tailscaled 经 `VpnService.protect` 保护的 socket 转发到系统解析器，行为等价。代价是这些 DNS 查询对本地网络可见，解析结果按本地网络而非出口节点所在地给出。

### 状态文件与状态接口

代理每次刷新后写 `/data/adb/shellscaled/run/magicdns.json`（0600，先写 `.new` 再改名）：

```json
{"updated":<unix 秒>,"mode":"off|tailnet|exit-doh|exit-passthrough","enabled":true,"noString":false,"rules":true,"rules6":true,"privateDns":"off|opportunistic|hostname|unknown","upstream":"system|exit-node|passthrough","daemon":"<BackendState>","exitNode":"<prefs.ExitNodeID>"}
```

`upstream` 为 `system`（`off` / `tailnet`，普通名称不经代理）、`exit-node`（`exit-doh`）或 `passthrough`（`exit-passthrough`，按原目的地址转发）。`enabled` 是本次刷新时开关文件是否存在，`noString` 表示内核没有 `xt_string`。`rules` / `rules6` 表示 IPv4 / IPv6 nat 链内容是否按期望应用（`rules6` 在 `[::1]:1053` 无法监听或监听已停止时为 false；任一为 false 且模式不是 `off` 时，WebUI 显示 MagicDNS 规则未生效）；`-F` 或任一 `-A` 失败时该链被清空，DNS 留在系统路径而不会被黑洞，错误按不同内容各记一行到 `daemon.log`，每次刷新重试直到成功。`privateDns` 每 30 秒用只读的 `cmd settings get global private_dns_mode` 读取。`daemon` / `exitNode` 记录本次决定所依据的 BackendState（LocalAPI 失败时为空）和 prefs 中的出口节点 ID（未选择或未 Running 时为空）；除此之外文件不含后缀、查询名、PeerAPI 地址或解析器地址。RPC `status` 响应的 `magicDNS` 字段转发这些值，并以 `updated` 在 20 秒内作为 `running`；`running` 时若 `enabled` 与当前开关不同、`daemon` 与当前 BackendState 不同，或 Running 时 `exitNode` 与当前 prefs 不同，则附加 `pending: true`，WebUI 显示“MagicDNS 正在更新…”并在 2 秒后重新拉取状态，而不是显示旧模式。连接 / 断开和切换出口节点后 helper 都会用 SIGUSR1 让代理立即刷新。

### 限制

- Private DNS 为指定主机名，或自动模式下 DoT 验证成功时，netd 使用 tcp/853，不产生 UDP/53，tailnet 名称无法解析。模块不修改 Private DNS，也不阻断 DoT。
- 自带 DoH 的应用（如 Chrome 安全 DNS）绕过拦截。
- 不解析短名称（只解析完整 FQDN）。
- 不拦截 TCP/53 查询。
- 开启 MagicDNS 之前刚查询失败的 tailnet 名称，开启后仍会失败几分钟：Android 的 DNS resolver 缓存了否定应答，模块无法清除该缓存（`ndc resolver flushnet` 在新版 Android 上已不可用）。两台实机上缓存都在数分钟内过期。
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

默认 `netfilter-mode=off`，避免向 Android 的防火墙链插入桌面 Linux 规则。网络角色操作保留现有 NetfilterMode，严格反向路径检查设备的限制见“出口节点”一节。普通本机 Tailnet 连通与转发场景分别验收；没有转发验证不能宣称热点、子网路由器或出口节点可用。

[Android SELinux 文档](https://source.android.com/docs/security/features/selinux) 明确指出 root 也受强制访问控制约束。模拟器测试保持 enforcing，并检查 AVC；不能用切换 permissive 代替权限适配。

## 出口节点

### 路由

选择获批准的出口节点后，tailscaled 在 table 52 计算 IPv4 / IPv6 默认路由；下文 IPv6 开关可以移除 IPv6 默认路由。默认路由存在时，13001 后备规则只接管未显式绑定网络、未受保护且不含自有 bypass 位的流量；匹配更早的 Android UID / VPN 规则的流量仍按该规则处理。显式绑定或 protected socket、tailscaled 自身 `0x10020000` socket 保留 Android 路由。出口节点离线时隧道内流量可能不可达，不能把规则安装视为端到端连接成功，也不保证所有流量都经过出口节点。开启前已建立的连接由下文“失效连接重置”在代理观测到变化后处理。

已建立连接改路由到 `tailscale0` 后源地址仍是 Wi-Fi / 蜂窝地址，出口节点以 `disallowed source` 丢弃，长连接会挂起直到超时，所以代理观测到变化后重置这类连接，让 App 重连。

`exit-node-allow-lan-access` 开启时，本地接口前缀可以成为 table 52 的 throw 路由，查表失败后继续 Android 后续规则；关闭时，上游可能把这些前缀送入出口节点。它不会改变显式选网、protected 或 UID 规则。[Android 17 NetworkController](https://android.googlesource.com/platform/system/netd/+/android-17.0.0_r1/server/NetworkController.cpp) 和 [DnsResolver](https://android.googlesource.com/platform/packages/modules/DnsResolver/+/android-17.0.0_r1/res_send.cpp) 会给网络解析请求的 UDP / TCP socket 设置受保护 DNS mark；新版后备 mask 排除这类流量，不能再把 Android 网络 DNS 笼统视为出口节点流量。

### 失效连接重置

路由观测独立于可选的 DNS 拦截：MagicDNS 关闭、请求记录无效或 DNS 规则应用失败都不会阻止连接重置。重置批次在 MagicDNS 请求锁释放后执行，不会阻塞开关请求获取该锁。

Android 在 VPN 的 UID 范围变化时（`ConnectivityService.updateVpnUidRanges` → `maybeCloseSockets`）调用 netd `destroyLiveTcpSockets`，对受影响 UID 的 TCP socket 发送 `SOCK_DESTROY`，只豁免 VPN App 自身的 UID。官方 Tailscale App 切换出口节点只是原地更新路由，已有连接被黑洞直到超时。

本模块在 MagicDNS 代理进程中按下述规则选择 TCP 连接重连，范围与 Android 按 UID 批量重置不同。每次刷新（5 秒，以及连接 / 断开、切换出口节点或 IPv6 开关后的 SIGUSR1）记录路由状态：出口节点 = BackendState 为 Running 时 prefs 的 `ExitNodeID`；exit6 = 选择了出口节点且 IPv6 opt-out 文件不存在；allowLAN = 选择了出口节点且 `ExitNodeAllowLANAccess`。LocalAPI `status` 或 `prefs` 读取失败时状态未知，不记录。状态与上次不同时（代理启动后的首次观测也算），代理用 `NETLINK_SOCK_DIAG` 列出 IPv4 / IPv6 中除 LISTEN、CLOSE、TIME_WAIT 外的 TCP socket 及其 `INET_DIAG_MARK`，对每个不豁免的 socket 用 `RTM_GETROUTE`（`RTM_F_FIB_MATCH`，带 socket 的源 / 目标地址、TCP 协议和源 / 目标端口、mark、UID 和绑定接口；协议和端口让按 `ipproto` / `sport` / `dport` 匹配的 ip rule 生效，4.17 之前的内核忽略协议和端口）向内核查询它的包现在命中的路由，对选中的连接发送 `SOCK_DESTROY`（socket id 原样取自内核的返回，同 Android `InetDiagMessage`），重置数大于 0 或出错时向 `daemon.log` 记一行 `exit node routing changed: reset N affected TCP connections`。查询会计入 Android 在 13001 之前的 UID / VPN 规则、local 表、子网路由、allow-LAN throw 路由和 IPv6 开关。它不提供连接建立时间，也不能证明所有网络策略下的端到端可达性；每次观察到状态变化只处理一批连接，状态未再变化时不会重复清理：

- 豁免：mark 含 `0x10030000` 任一位（daemon / 代理 socket、显式绑定、`protectedFromVpn`）；内核未返回 mark 的 socket；路由查询失败的 socket。
- Tailnet 源：源地址是 `tailscale0` 上的地址；运营商 CGNAT 的 `100.x` rmnet 地址因此按物理源处理。v4-mapped 地址按 IPv4 处理。
- 命中 local 表（目标为本机接口地址）的连接保留；其余连接只有在“经 `tailscale0` 出去”与“Tailnet 源”一致时可用，不一致即重置：出口节点开启后的物理源连接、关闭后或 allow-LAN 开启后改走物理接口的 Tailnet 源连接。
- 从一个出口节点切换到另一个时，经出口默认路由（前缀长度 0）进入 `tailscale0` 的 Tailnet 源连接也被重置：新出口节点没有旧连接的 NAT 状态。观测存在延迟（通常由 SIGUSR1 唤醒，否则最多等待下一次 5 秒刷新，探测或系统调用还可能增加等待），在新出口已经建立的连接也可能被这一批处理重置，需要再次重连。该批次不额外重置子网路由和 Tailnet 目标的连接。

与 Android 相同只处理 TCP；UDP 流不受影响。需要内核 `CONFIG_INET_DIAG_DESTROY`、`CAP_NET_ADMIN`（代理以 root 运行）以及 SELinux 允许 sock_diag 写操作；不满足时只记一行错误，路由行为不变。

### DNS

`accept-dns=false` 时 quad-100 解析器只回答 MagicDNS 节点名，不向任何上游转发（`dnsConfigForNetmap` 在 CorpDNS 关闭时提前返回），因此出口节点下的普通 DNS 无法由 tailscaled 提供。开启 MagicDNS 后，出口节点下由下述代理整体接管 UDP/53；未开启时普通 DNS 照常由 netd 发往当前网络的解析器，不经过出口节点，WebUI 在网络角色卡片中提示这一点。

开启 MagicDNS 时，出口节点下的 DNS 由代理处理：出口节点的 PeerAPI DoH 探测成功时进入 `exit-doh`，普通名称经出口节点解析；探测失败时进入 `exit-passthrough`，普通名称由代理带 `SO_MARK=0x10020000` 转发给系统原本选定的解析器，绕过 12500/13001 与 table 52，与 allow-LAN 设置无关（见上文 MagicDNS 机制与原目的地址转发）。tailnet 名称始终走 LocalAPI `dns-query`，不受 mark 影响。

模块判定“出口节点生效”的条件与状态字段 `exitNodeConfigured` 相同：prefs 中 `ExitNodeID != ""`，或 `ExitNodeIP` 非空且不等于 `invalid IP`。WebUI 始终提交 StableNodeID，因此 `ExitNodeStatus.ID` 可用于判定。

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
grep 'disabling tunneled IPv6\|allowing exit node access to local IPs\|exit node routing changed' /data/adb/shellscaled/logs/daemon.log
ls -l /data/adb/shellscaled/exit-node-ipv6-local
ip route get 1.1.1.1 uid 10123                 # 结果取决于实际 UID / VPN 规则
ss -tn state established | wc -l               # 切换出口节点前后对比
```

期望：切换出口节点后 `daemon.log` 有一行 `exit node routing changed: reset N affected TCP connections`（有可重置的连接时），且没有该行的失败形式。

开启出口节点后分别在 Wi-Fi 和蜂窝下解析公共域名、打开网页、确认 IP 查询站点显示出口节点地址、解析 MagicDNS 名称；关闭出口机器后应显示离线提示，“关闭出口节点”一次即可恢复。

### 未实现与不支持

- 失效连接重置只处理本机 TCP socket；UDP 流和热点 / 转发流量不处理。转发包在出口节点开启时是否被 13001 捕获取决于其 fwmark，未经设备验收。

## 上游与维护

原模块当前 main 最后提交为 `dffc1aee6fa80dd435a95776a37f979e0204e590`，日期 2025-11-04；其 README 仍描述历史 userspace + hev 方案，而当时代码已采用 Android CLI。因此重构以实际源码为依据。

当前模块跟随官方 [Tailscale v1.104.6](https://github.com/tailscale/tailscale/releases/tag/v1.104.6) release 源码，选择相同版本作为模块版本。源码仓库保留官方 release 的历史，再独立维护 Android 补丁，避免把 Android 改动混入上游提交。没有 clone AOSP；仅下载必要的官方单文件。

端口和 UID 策略的内核回归使用原生 netlink 配置合成规则，不依赖 Android `ip` 是否支持 `ipproto` / `dport` 参数。仅在 Medium_Phone 的独立 network namespace 中设置 `SHELLSCALED_NETLINK_TEST=1` 运行 helper 的 `TestRouteLookupPortRule`；测试拒绝 init network namespace，并精确清理自己的规则和接口。
