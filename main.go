package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	var opts options
	flag.StringVar(&opts.configPath, "config", "data/config.yaml", "YAML configuration file")
	flag.StringVar(&opts.webAddr, "web", "127.0.0.1:0", "Web listening address (0 selects a free port)")
	flag.StringVar(&opts.dnsAddr, "dns", ":53", "IPv4 DNS listening address")
	flag.StringVar(&opts.rtmpAddr, "rtmp", ":1935", "RTMP listening address")
	flag.StringVar(&opts.mediaPath, "mediamtx", "mediamtx", "MediaMTX executable")
	flag.BoolVar(&opts.bridge, "bridge", false, "Docker bridge mode; use the host LAN address")
	flag.Parse()
	opts.hostIP = os.Getenv("RELAY_HOST_IP")
	for label, addr := range map[string]string{"Web": opts.webAddr, "DNS": opts.dnsAddr, "RTMP": opts.rtmpAddr} {
		if err := validListen(label, addr); err != nil {
			return err
		}
	}

	password := os.Getenv("RELAY_PASSWORD")
	host, _, _ := net.SplitHostPort(opts.webAddr)
	if password == "" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return errors.New("对局域网开放管理页时请设置 RELAY_PASSWORD")
	}
	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return fmt.Errorf("加载 %s: %w", opts.configPath, err)
	}
	web, err := net.Listen("tcp", opts.webAddr)
	if err != nil {
		return fmt.Errorf("Web 监听: %w", err)
	}

	opts.webAddr = web.Addr().String()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := newRelay(opts, cfg)
	go a.runDNS(ctx)
	go a.probeUpstream(ctx)
	if !opts.bridge {
		go func() {
			tick := time.NewTicker(15 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					a.refreshAddress()
				}
			}
		}()
	}
	mediaDone := make(chan struct{})
	go func() {
		defer close(mediaDone)
		a.runMedia(ctx)
	}()
	srv := &http.Server{
		Handler: a.handler(password), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("Web 管理页监听 %s", web.Addr())
	err = srv.Serve(web)
	stop()
	<-mediaDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "media-event" {
		err = mediaEvent()
	} else {
		err = run()
	}
	if err != nil {
		log.Fatal(err)
	}
}
