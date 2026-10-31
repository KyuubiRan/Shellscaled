package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"syscall"
)

// After an exit-node routing change, TCP flows that can no longer work are
// reset with SOCK_DESTROY so apps reconnect over the new path, as Android's
// ConnectivityService does with destroyLiveTcpSockets when VPN UID ranges
// change. Sockets the daemon owns or that netd marks explicitly selected or
// protectedFromVpn never enter table 52 and are left alone. For every other
// socket the kernel is asked which route its packets take now.

// exitRouting is the exit-node state whose changes trigger a sweep.
type exitRouting struct {
	node            string // prefs.ExitNodeID
	exit6, allowLAN bool
}

// tcpFlow is what a sock_diag dump reports about a socket.
type tcpFlow struct {
	src, dst       netip.Addr
	mark, uid, oif uint32
}

// route is the FIB entry the kernel matches for a flow.
type route struct {
	typ, dstLen byte
	oif         uint32
}

const (
	sockDiagByFamily = 20
	sockDestroy      = 21
	inetDiagMark     = 15
	afInet           = 2
	afInet6          = 10
	rtaMark          = 16
	rtaUID           = 25
	rtmFFibMatch     = 0x2000
	// ESTABLISHED, SYN_SENT, SYN_RECV, FIN_WAIT1/2, CLOSE_WAIT, LAST_ACK, CLOSING.
	tcpLiveStates = 1<<1 | 1<<2 | 1<<3 | 1<<4 | 1<<5 | 1<<8 | 1<<9 | 1<<11
)

// classify reports whether a flow is broken on its current route: outside
// the local table it works only if it leaves through tailscale0 exactly when
// its source is a tailscale0 address. After switching exit nodes, flows on
// the exit default route break too: the new node has no NAT state for them.
func classify(tsSrc bool, rt route, tsIndex uint32, switched bool) bool {
	viaTS := rt.oif == tsIndex
	return rt.typ != syscall.RTN_LOCAL && (viaTS != tsSrc || switched && viaTS && rt.dstLen == 0)
}

// observeExitRouting sweeps once whenever the routing differs from the last
// observation; the first observation counts as a change.
func (p *magicDNSProxy) observeExitRouting(r exitRouting) {
	if p.routing != nil && *p.routing == r {
		return
	}
	switched := p.routing != nil && p.routing.node != "" && r.node != "" && p.routing.node != r.node
	p.routing = &r
	switch n, err := p.sweep(switched); {
	case err != nil:
		fmt.Fprintf(os.Stderr, "exit node routing changed: reset %d stale TCP connections, then failed: %v\n", n, err)
	case n > 0:
		fmt.Fprintf(os.Stderr, "exit node routing changed: reset %d stale TCP connections\n", n)
	}
}

// routeReq builds an RTM_GETROUTE request for the FIB entry f's packets match.
func routeReq(f tcpFlow) []byte {
	family := byte(afInet6)
	if f.dst.Is4() {
		family = afInet
	}
	attrs := slices.Concat(nlAttr(syscall.RTA_DST, f.dst.AsSlice()), nlAttr(syscall.RTA_SRC, f.src.AsSlice()),
		nlAttr(rtaMark, binary.NativeEndian.AppendUint32(nil, f.mark)), nlAttr(rtaUID, binary.NativeEndian.AppendUint32(nil, f.uid)))
	if f.oif != 0 {
		attrs = append(attrs, nlAttr(syscall.RTA_OIF, binary.NativeEndian.AppendUint32(nil, f.oif))...)
	}
	msg := binary.NativeEndian.AppendUint32(nil, uint32(16+12+len(attrs)))
	msg = binary.NativeEndian.AppendUint16(msg, syscall.RTM_GETROUTE)
	msg = binary.NativeEndian.AppendUint16(msg, syscall.NLM_F_REQUEST)
	msg = append(msg, make([]byte, 8)...)                     // seq, pid
	msg = append(msg, family, 0, 0, 0, 0, 0, 0, 0)            // rtmsg up to rtm_type
	msg = binary.NativeEndian.AppendUint32(msg, rtmFFibMatch) // rtm_flags
	return append(msg, attrs...)
}

// parseRoute reads an RTM_NEWROUTE reply.
func parseRoute(msg []byte) (route, error) {
	if len(msg) < 28 || binary.NativeEndian.Uint16(msg[4:]) != syscall.RTM_NEWROUTE {
		return route{}, nlErrno(msg)
	}
	rt := route{typ: msg[23], dstLen: msg[17]}
	if oif := nlFind(msg[28:], syscall.RTA_OIF); len(oif) == 4 {
		rt.oif = binary.NativeEndian.Uint32(oif)
	}
	return rt, nil
}

// inetDiagReq builds a netlink message carrying a TCP inet_diag_req_v2; id is
// the 48-byte inet_diag_sockid.
func inetDiagReq(typ, flags uint16, family byte, states uint32, id []byte) []byte {
	msg := binary.NativeEndian.AppendUint32(nil, 16+56)
	msg = binary.NativeEndian.AppendUint16(msg, typ)
	msg = binary.NativeEndian.AppendUint16(msg, flags)
	msg = append(msg, make([]byte, 8)...) // seq, pid
	msg = append(msg, family, syscall.IPPROTO_TCP, 0, 0)
	msg = binary.NativeEndian.AppendUint32(msg, states)
	return append(msg, id...)
}

// destroyReq turns an inet_diag_msg into the SOCK_DESTROY request for that
// socket, with the id exactly as the kernel returned it (Android's
// InetDiagMessage does the same).
func destroyReq(diag []byte) []byte {
	return inetDiagReq(sockDestroy, 0x5, diag[0], tcpLiveStates, diag[4:52]) // NLM_F_REQUEST|NLM_F_ACK
}

// parseInetDiag reads the addresses (v4-mapped ones as IPv4), bound
// interface, owner and mark of an inet_diag_msg; without the mark the socket
// cannot be classified.
func parseInetDiag(diag []byte) (f tcpFlow, ok bool) {
	if len(diag) < 72 {
		return f, false
	}
	n := 16
	if diag[0] == afInet {
		n = 4
	}
	f.src, _ = netip.AddrFromSlice(diag[8 : 8+n])
	f.dst, _ = netip.AddrFromSlice(diag[24 : 24+n])
	f.src, f.dst = f.src.Unmap(), f.dst.Unmap()
	f.oif, f.uid = binary.NativeEndian.Uint32(diag[40:]), binary.NativeEndian.Uint32(diag[64:])
	m := nlFind(diag[72:], inetDiagMark)
	if len(m) != 4 {
		return f, false
	}
	f.mark = binary.NativeEndian.Uint32(m)
	return f, true
}

// nlErrno decodes an NLMSG_ERROR message; error 0 is an ACK.
func nlErrno(msg []byte) error {
	if len(msg) < 20 || binary.NativeEndian.Uint16(msg[4:]) != 2 {
		return errors.New("unexpected netlink reply")
	}
	if e := -int32(binary.NativeEndian.Uint32(msg[16:])); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}

func netlinkSocket(proto int) (int, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, proto)
	if err == nil {
		_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 1})
	}
	return fd, err
}

// sweepStaleFlows destroys the TCP sockets classify marks as broken. A socket
// whose route lookup fails is kept.
func sweepStaleFlows(switched bool) (int, error) {
	ts, err := net.InterfaceByName("tailscale0")
	if err != nil {
		return 0, err
	}
	addrs, _ := ts.Addrs()
	var tsAddrs []netip.Addr
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			addr, _ := netip.AddrFromSlice(n.IP)
			tsAddrs = append(tsAddrs, addr.Unmap())
		}
	}
	fd, err := netlinkSocket(4) // NETLINK_SOCK_DIAG
	if err != nil {
		return 0, err
	}
	defer syscall.Close(fd)
	rfd, err := netlinkSocket(syscall.NETLINK_ROUTE)
	if err != nil {
		return 0, err
	}
	defer syscall.Close(rfd)
	var addr syscall.Sockaddr = &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	buf := make([]byte, 64<<10)
	type candidate struct {
		f   tcpFlow
		req []byte
	}
	var flows []candidate
	for _, family := range []byte{afInet, afInet6} {
		if err := syscall.Sendto(fd, inetDiagReq(sockDiagByFamily, 0x301, family, tcpLiveStates, make([]byte, 48)), 0, addr); err != nil { // NLM_F_REQUEST|NLM_F_DUMP
			return 0, err
		}
	dump:
		for {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				return 0, err
			}
			for b := buf[:n]; len(b) >= 16; {
				size := int(binary.NativeEndian.Uint32(b))
				if size < 16 || size > len(b) {
					return 0, errors.New("bad sock_diag reply")
				}
				switch binary.NativeEndian.Uint16(b[4:]) {
				case 3: // NLMSG_DONE
					break dump
				case 2:
					return 0, nlErrno(b[:size])
				case sockDiagByFamily:
					if f, ok := parseInetDiag(b[16:size]); ok && f.mark&0x10030000 == 0 {
						flows = append(flows, candidate{f, destroyReq(b[16:size])})
					}
				}
				b = b[min((size+3)&^3, len(b)):]
			}
		}
	}
	reset := 0
	for _, c := range flows {
		if err := syscall.Sendto(rfd, routeReq(c.f), 0, addr); err != nil {
			return reset, err
		}
		n, _, err := syscall.Recvfrom(rfd, buf, 0)
		if err != nil {
			return reset, err
		}
		rt, err := parseRoute(buf[:n])
		if err != nil || !classify(slices.Contains(tsAddrs, c.f.src), rt, uint32(ts.Index), switched) {
			continue
		}
		if err := syscall.Sendto(fd, c.req, 0, addr); err != nil {
			return reset, err
		}
		if n, _, err = syscall.Recvfrom(fd, buf, 0); err != nil {
			return reset, err
		}
		// ENOENT: the socket closed after the dump.
		if err := nlErrno(buf[:n]); err == nil {
			reset++
		} else if !errors.Is(err, syscall.ENOENT) {
			return reset, err
		}
	}
	return reset, nil
}
