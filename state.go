package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type options struct {
	configPath, webAddr, dnsAddr, rtmpAddr, mediaPath string
	hostIP                                            string
	bridge                                            bool
}

type event struct {
	Time  time.Time `json:"time"`
	Stage int       `json:"stage"`
	Text  string    `json:"text"`
}

type check struct {
	Title  string `json:"title"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

type state struct {
	Config     config   `json:"config"`
	Revision   uint64   `json:"revision"`
	Address    string   `json:"address"`
	Bridge     bool     `json:"bridge"`
	DNSPort    string   `json:"dnsPort"`
	RTMPPort   string   `json:"rtmpPort"`
	WebPort    string   `json:"webPort"`
	Interfaces []string `json:"interfaces"`
	Bound      string   `json:"bound"`
	Output     bool     `json:"output"`
	Testing    bool     `json:"testing"`
	Checks     []check  `json:"checks"`
	Events     []event  `json:"events"`
}

type liveSession struct {
	path, sourceID, alias string
}

type relay struct {
	opts                          options
	mu                            sync.Mutex
	cfg                           config
	revision                      uint64
	changed                       chan struct{}
	probe                         chan struct{}
	address                       string
	interfaces                    []string
	dnsReady                      bool
	dnsError                      string
	upstreamState, upstreamDetail string
	lastRewrite                   time.Time
	testStart, testEnd            time.Time
	mediaState, mediaDetail       string
	session                       liveSession
	output                        bool
	outputError                   string
	events                        []event
}

func newRelay(opts options, cfg config) *relay {
	if opts.bridge && lanIP(opts.hostIP) {
		cfg.DNS.LANIP = opts.hostIP
	}
	a := &relay{
		opts: opts, cfg: cfg, changed: make(chan struct{}), probe: make(chan struct{}, 1),
		upstreamState: "waiting", upstreamDetail: "等待上游解析检测",
		mediaState: "waiting", mediaDetail: "正在启动 MediaMTX",
	}
	a.refreshAddress()
	return a
}

func (a *relay) notify() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *relay) logEvent(stage int, message string) {
	a.events = append(a.events, event{time.Now(), stage, message})
	if len(a.events) > 60 {
		a.events = a.events[len(a.events)-60:]
	}
	a.notify()
}

func (a *relay) refreshAddress() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshAddressLocked()
}

func (a *relay) refreshAddressLocked() {
	address, names := discoverAddress(a.opts.bridge, a.cfg.DNS)
	if address != a.address {
		a.address = address
		a.lastRewrite = time.Time{}
		if address != "" {
			a.logEvent(1, "接收器地址已更新为 "+address)
		} else {
			a.logEvent(1, "等待设置接收器局域网地址")
		}
	}
	if !slices.Equal(names, a.interfaces) {
		a.interfaces = names
		a.notify()
	}
}

func discoverAddress(bridge bool, cfg networkConfig) (string, []string) {
	var address string
	var names []string
	if bridge {
		return cfg.LANIP, nil
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip := strings.SplitN(addr.String(), "/", 2)[0]
			if !lanIP(ip) {
				continue
			}
			names = append(names, iface.Name)
			if cfg.Interface == iface.Name || (cfg.Interface == "" && address == "") {
				address = ip
			}
			break
		}
	}
	if cfg.LANIP != "" {
		return cfg.LANIP, names
	}
	if cfg.Interface == "" {
		target := cfg.Upstream
		if target == "" {
			target = "192.0.2.1" // Select the default route without sending a packet.
		}
		conn, err := net.Dial("udp4", net.JoinHostPort(target, "53"))
		if err == nil {
			ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
			conn.Close()
			if lanIP(ip) {
				address = ip
			}
		}
	}
	return address, names
}

var errStale = errors.New("配置已更新，请重新打开设置或直播对象后再保存")

func (a *relay) update(revision uint64, cfg config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if revision != a.revision {
		return errStale
	}
	if a.session.alias != "" && !slices.ContainsFunc(cfg.Streams, func(s stream) bool { return s.ID == a.session.alias }) {
		return errors.New("这个对象正在接收直播，请结束本次直播后删除")
	}
	if err := saveConfig(a.opts.configPath, cfg); err != nil {
		log.Printf("save config: %v", err)
		return errors.New("配置保存失败，请检查数据目录权限和磁盘空间")
	}
	upstreamChanged := a.cfg.DNS.Upstream != cfg.DNS.Upstream
	a.cfg = cfg
	a.revision++
	a.refreshAddressLocked()
	if upstreamChanged {
		a.upstreamState, a.upstreamDetail = "waiting", "正在检测新的上游 DNS"
		select {
		case a.probe <- struct{}{}:
		default:
		}
	}
	a.logEvent(1, "配置已保存")
	return nil
}

func port(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

func (a *relay) snapshot() (state, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	testing := now.Before(a.testEnd)
	testExpired := !a.testEnd.IsZero() && !testing
	dns := check{"接收服务", a.mediaState, a.mediaDetail, "检查 DNS 端口、RTMP 端口和容器日志，确认端口没有被其他服务占用。"}
	switch {
	case a.dnsError != "":
		dns.State, dns.Detail = "error", a.dnsError
	case !a.dnsReady:
		dns.State, dns.Detail = "waiting", "正在启动 DNS 监听"
	case a.mediaState == "ok":
		dns.Detail = "DNS UDP/TCP 与 RTMP 已就绪，MediaMTX API 正常"
	}
	upstream := check{"上游网络", a.upstreamState, a.upstreamDetail, "在设置中填写路由器或加速器提供的 DNS，再运行检测。"}
	rewrite := check{"PS5 DNS 分流", "waiting", "等待 Twitch 推流域名查询", "在 PS5 的网络高级设置中填写总览显示的首选 DNS，再开启 Twitch 直播。"}
	switch {
	case a.address == "":
		rewrite.Detail = "等待设置接收器局域网地址"
	case !a.lastRewrite.IsZero() && (a.session.path != "" || a.lastRewrite.After(now.Add(-5*time.Minute)) || (testExpired && !a.lastRewrite.Before(a.testStart))):
		rewrite.State, rewrite.Detail = "ok", "已将 Twitch 推流域名解析到 "+a.address
	case testExpired && a.session.path == "" && dns.State == "ok" && upstream.State == "ok":
		rewrite.State, rewrite.Detail = "error", "检测期间未观察到 Twitch 推流域名查询"
	}
	video := check{"视频流接收", "waiting", "等待 PS5 开启 Twitch 直播", "确认 PS5 已绑定 Twitch 并开始直播，检查局域网防火墙是否允许 RTMP 端口。"}
	output := check{"直播对象输出", "waiting", "等待发布到固定别名", "确认已选中直播对象，使用该对象的 RTMP 地址在 OBS 添加媒体源。"}
	if a.session.path != "" {
		video.State, video.Detail = "ok", "MediaMTX 已收到 RTMP 视频流"
		switch {
		case a.session.alias == "":
			output.Detail = "本次开播时未选择直播对象"
		case a.output:
			output.State, output.Detail = "ok", "可通过 "+a.session.alias+" 拉取音视频流"
		case a.outputError != "":
			output.State, output.Detail = "error", a.outputError
		}
	} else if testExpired && rewrite.State == "ok" && dns.State == "ok" {
		video.State, video.Detail = "error", "检测结束时未收到 RTMP 视频流"
	}
	return state{
		Config: a.cfg, Revision: a.revision, Address: a.address, Bridge: a.opts.bridge, DNSPort: port(a.opts.dnsAddr),
		RTMPPort: port(a.opts.rtmpAddr), WebPort: port(a.opts.webAddr), Interfaces: a.interfaces,
		Bound: a.session.alias, Output: a.output, Testing: testing,
		Checks: []check{dns, upstream, rewrite, video, output}, Events: slices.Clone(a.events),
	}, a.changed
}

func (a *relay) startTest() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.testEnd) {
		return
	}
	a.testStart = time.Now()
	a.testEnd = a.testStart.Add(45 * time.Second)
	if a.session.path == "" {
		a.lastRewrite = time.Time{}
	}
	a.logEvent(1, "已开始 45 秒连接检测")
	select {
	case a.probe <- struct{}{}:
	default:
	}
	time.AfterFunc(time.Until(a.testEnd), func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.logEvent(1, "连接检测已结束")
		if a.session.path == "" && a.lastRewrite.IsZero() && a.address != "" {
			a.logEvent(3, "检测期间未观察到推流域名查询")
		} else if a.session.path == "" && !a.lastRewrite.IsZero() {
			a.logEvent(4, "检测结束时未收到 RTMP 视频流")
		}
	})
}

func validListen(label, addr string) error {
	_, p, err := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 || (n == 0 && label != "Web") {
		return fmt.Errorf("%s 必须指定有效的监听地址和端口", label)
	}
	return nil
}
