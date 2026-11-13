package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Match the RTMP hosts in Twitch's public ingest list, plus its legacy endpoint.
func ingestDomain(name string) bool {
	host := strings.ToLower(dns.Fqdn(name))
	return host == "live.twitch.tv." ||
		strings.HasSuffix(host, ".contribute.live-video.net.") ||
		strings.HasSuffix(host, ".global-contribute.live-video.net.")
}

func (a *relay) serveDNS(w dns.ResponseWriter, query *dns.Msg) {
	reply := new(dns.Msg)
	remote, _ := netip.ParseAddrPort(w.RemoteAddr().String())
	ip := remote.Addr().Unmap()
	if !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		w.WriteMsg(reply.SetRcode(query, dns.RcodeRefused))
		return
	}
	if query.Opcode != dns.OpcodeQuery || len(query.Question) != 1 {
		w.WriteMsg(reply.SetRcode(query, dns.RcodeFormatError))
		return
	}

	q := query.Question[0]
	a.mu.Lock()
	address, upstream := a.address, a.cfg.DNS.Upstream
	a.mu.Unlock()
	if q.Qclass == dns.ClassINET && ingestDomain(q.Name) {
		if address == "" {
			w.WriteMsg(reply.SetRcode(query, dns.RcodeServerFailure))
			return
		}
		reply.SetReply(query)
		reply.RecursionAvailable = true
		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
			reply.Answer = []dns.RR{&dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.ParseIP(address).To4(),
			}}
			if w.WriteMsg(reply) == nil {
				a.mu.Lock()
				if a.address == address {
					first := a.lastRewrite.IsZero() || time.Since(a.lastRewrite) > 5*time.Minute
					a.lastRewrite = time.Now()
					if first {
						a.logEvent(3, "已重写 "+strings.ToLower(q.Name)+" → "+address)
					}
				}
				a.mu.Unlock()
			}
			return
		}
		w.WriteMsg(reply)
		return
	}

	_, tcp := w.RemoteAddr().(*net.TCPAddr)
	result, _, err := a.exchangeDNS(query, upstream, address, tcp)
	if err != nil {
		w.WriteMsg(reply.SetRcode(query, dns.RcodeServerFailure))
		return
	}
	if !tcp {
		size := dns.MinMsgSize
		if opt := query.IsEdns0(); opt != nil {
			size = max(size, int(opt.UDPSize()))
		}
		result.Truncate(size)
	}
	w.WriteMsg(result)
}

func systemDNS() []string {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	return cfg.Servers
}

func (a *relay) exchangeDNS(query *dns.Msg, override, address string, tcp bool) (*dns.Msg, string, error) {
	servers := systemDNS()
	if override != "" {
		servers = []string{override}
	}
	client := dns.Client{Timeout: 1500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	lastErr := errors.New("没有可用的上游 DNS")
	for _, server := range servers {
		if a.localDNS(server, address) {
			continue
		}
		client.Net = "udp"
		if tcp {
			client.Net = "tcp"
		}
		target := net.JoinHostPort(server, "53")
		reply, _, err := client.ExchangeContext(ctx, query, target)
		if err == nil && reply.Truncated && !tcp {
			client.Net = "tcp"
			reply, _, err = client.ExchangeContext(ctx, query, target)
		}
		if err == nil && reply.Rcode != dns.RcodeServerFailure && reply.Rcode != dns.RcodeRefused {
			return reply, server, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	return nil, "", lastErr
}

func (a *relay) localDNS(server, address string) bool {
	host, p, _ := net.SplitHostPort(a.opts.dnsAddr)
	if p != "53" || (a.opts.bridge && server == "127.0.0.11") {
		return false
	}
	if server == address || (host != "" && server == host) {
		return true
	}
	ip, err := netip.ParseAddr(server)
	if err != nil || !ip.Is4() || (host != "" && host != "0.0.0.0") {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if local, _, err := net.ParseCIDR(addr.String()); err == nil && local.Equal(net.ParseIP(server)) {
			return true
		}
	}
	return false
}

func (a *relay) probeUpstream(ctx context.Context) {
	for {
		a.mu.Lock()
		upstream, address := a.cfg.DNS.Upstream, a.address
		a.mu.Unlock()
		query := new(dns.Msg)
		query.SetQuestion("www.playstation.com.", dns.TypeA)
		start := time.Now()
		reply, server, err := a.exchangeDNS(query, upstream, address, false)
		label := upstream
		if label == "" {
			label = "系统 DNS"
		}
		status, detail := "error", label+" 未返回有效的 PlayStation 域名解析"
		if err == nil && reply.Rcode == dns.RcodeSuccess {
			for _, rr := range reply.Answer {
				if _, ok := rr.(*dns.A); ok {
					status = "ok"
					detail = fmt.Sprintf("%s（%s）已解析 PlayStation 域名 · %d ms", label, server, time.Since(start).Milliseconds())
					break
				}
			}
		}
		a.mu.Lock()
		if a.cfg.DNS.Upstream == upstream {
			a.upstreamState, a.upstreamDetail = status, detail
			a.logEvent(2, detail)
		}
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-a.probe:
		}
	}
}

func (a *relay) runDNS(ctx context.Context) {
	udp, err := net.ListenPacket("udp4", a.opts.dnsAddr)
	if err != nil {
		a.dnsFailed(err)
		return
	}
	tcp, err := net.Listen("tcp4", a.opts.dnsAddr)
	if err != nil {
		udp.Close()
		a.dnsFailed(err)
		return
	}
	started, done := make(chan struct{}, 2), make(chan error, 2)
	handler := dns.HandlerFunc(a.serveDNS)
	udpServer := &dns.Server{PacketConn: udp, Handler: handler, NotifyStartedFunc: func() { started <- struct{}{} }}
	tcpServer := &dns.Server{Listener: tcp, Handler: handler, NotifyStartedFunc: func() { started <- struct{}{} }}
	go func() { done <- udpServer.ActivateAndServe() }()
	go func() { done <- tcpServer.ActivateAndServe() }()
	<-started
	<-started
	a.mu.Lock()
	a.dnsReady = true
	a.logEvent(1, "DNS UDP/TCP 已监听端口 "+port(a.opts.dnsAddr))
	a.mu.Unlock()
	select {
	case <-ctx.Done():
	case err := <-done:
		a.dnsFailed(err)
	}
	udpServer.Shutdown()
	tcpServer.Shutdown()
}

func (a *relay) dnsFailed(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dnsReady = false
	a.dnsError = fmt.Sprintf("DNS 监听失败，端口 %s · %v", port(a.opts.dnsAddr), err)
	a.logEvent(1, a.dnsError)
}
