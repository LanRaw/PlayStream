package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

const hookEnv = "PS5_RELAY_HOOK_URL"

var ingestPath = regexp.MustCompile(`^app/[A-Za-z0-9_.~/-]+$`)
var mediaHTTP = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{}}

type mediaClient struct {
	base, rtmp    string
	alias, source string
	bindingTime   time.Time
}

type mediaPath struct {
	Name       string
	Available  bool
	OnlineTime time.Time
	Source     struct{ Type, ID string }
	Tracks     []string
}

func (m *mediaClient) request(ctx context.Context, method, path string, body io.Reader, dst any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, body)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("MediaMTX API HTTP %d", resp.StatusCode)
	}
	if dst != nil {
		err = json.NewDecoder(resp.Body).Decode(dst)
	}
	return resp.StatusCode, err
}

func (m *mediaClient) paths(ctx context.Context) ([]mediaPath, error) {
	var paths []mediaPath
	for page := 0; ; page++ {
		var result struct {
			Items     []mediaPath
			PageCount int
		}
		_, err := m.request(ctx, "GET", "/v3/paths/list?page="+strconv.Itoa(page), nil, &result)
		if err != nil {
			return nil, err
		}
		paths = append(paths, result.Items...)
		if page+1 >= result.PageCount {
			return paths, nil
		}
	}
}

func (m *mediaClient) setAlias(ctx context.Context, s liveSession) error {
	if m.alias != "" && (m.alias != s.alias || m.source != s.sourceID) {
		code, err := m.request(ctx, "DELETE", "/v3/config/paths/delete/"+m.alias, nil, nil)
		if err != nil && code != http.StatusNotFound {
			return err
		}
		m.alias, m.source = "", ""
	}
	if s.alias != "" && m.alias == "" {
		source := url.URL{Scheme: "rtmp", Host: m.rtmp, Path: "/" + s.path}
		body, _ := json.Marshal(map[string]any{"source": source.String(), "sourceOnDemand": false})
		bindingTime := time.Now()
		code, err := m.request(ctx, "POST", "/v3/config/paths/add/"+s.alias, bytes.NewReader(body), nil)
		if code == http.StatusBadRequest {
			// An earlier request may have been applied before the API connection closed.
			_, err = m.request(ctx, "POST", "/v3/config/paths/replace/"+s.alias, bytes.NewReader(body), nil)
		}
		if err != nil {
			return err
		}
		m.alias, m.source, m.bindingTime = s.alias, s.sourceID, bindingTime
	}
	return nil
}

func videoPath(p mediaPath) bool {
	return p.Available && slices.ContainsFunc(p.Tracks, func(codec string) bool {
		switch codec {
		case "H264", "H265", "AV1", "VP8", "VP9", "M-JPEG", "MPEG-4 Video", "MPEG-1/2 Video":
			return true
		}
		return false
	})
}

func (a *relay) reconcileMedia(ctx context.Context, m *mediaClient) (ready, pending bool) {
	paths, err := m.paths(ctx)
	if err != nil {
		return false, false
	}
	a.mu.Lock()
	if a.mediaState != "ok" {
		a.mediaState, a.mediaDetail = "ok", "RTMP 已监听端口 "+port(a.opts.rtmpAddr)
		a.logEvent(1, a.mediaDetail)
	}
	old := a.session
	paths = slices.DeleteFunc(paths, func(p mediaPath) bool {
		return !p.Available
	})
	sources := make([]mediaPath, 0, len(paths))
	for _, p := range paths {
		if p.Source.Type == "rtmpConn" && ingestPath.MatchString(p.Name) && videoPath(p) {
			sources = append(sources, p)
		}
	}
	if !slices.ContainsFunc(sources, func(p mediaPath) bool {
		return p.Name == old.path && p.Source.ID == old.sourceID
	}) {
		if old.path != "" {
			a.logEvent(4, "RTMP 视频源已断开")
			a.testEnd = time.Time{}
		}
		a.session = liveSession{}
		a.output, a.outputError = false, ""
		if len(sources) != 0 {
			slices.SortFunc(sources, func(x, y mediaPath) int { return x.OnlineTime.Compare(y.OnlineTime) })
			source := sources[0]
			a.session = liveSession{source.Name, source.Source.ID, a.cfg.Active}
			a.logEvent(4, "已收到 RTMP 视频流")
		}
	}
	s := a.session
	a.mu.Unlock()

	err = m.setAlias(ctx, s)
	output := err == nil && s.alias != "" && slices.ContainsFunc(paths, func(p mediaPath) bool {
		return p.Name == s.alias && p.Source.Type == "rtmpSource" && videoPath(p) && !p.OnlineTime.Before(m.bindingTime)
	})
	var outputError string
	if err != nil {
		outputError = "MediaMTX 未能更新直播对象映射"
	} else if s.alias != "" && !output && time.Since(m.bindingTime) > 8*time.Second {
		outputError = "已建立别名，但未读到视频。请检查 RTMP 连接后重新开播。"
	}
	a.mu.Lock()
	if output != a.output || outputError != a.outputError {
		a.output, a.outputError = output, outputError
		switch {
		case output:
			a.logEvent(5, "固定别名 "+s.alias+" 已可拉流")
		case outputError != "":
			a.logEvent(5, outputError)
		default:
			a.notify()
		}
	}
	a.mu.Unlock()
	return true, err != nil || (s.alias != "" && !output && outputError == "")
}

func (a *relay) mediaFailed(detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mediaState == "error" && a.mediaDetail == detail {
		return
	}
	a.mediaState, a.mediaDetail = "error", detail
	a.session = liveSession{}
	a.output, a.outputError = false, ""
	a.logEvent(1, detail)
}

func loopbackAddress(addr string) string {
	host, p, _ := net.SplitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, p)
}

func (a *relay) runMedia(ctx context.Context) {
	hook, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		a.mediaFailed("无法建立 MediaMTX 事件监听")
		return
	}
	signal := make(chan struct{}, 1)
	srv := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case signal <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(hook)
	defer srv.Close()

	executable, err := os.Executable()
	if err != nil {
		a.mediaFailed("无法确定接收器程序路径")
		return
	}
	dir, err := os.MkdirTemp("", "ps5-media-*")
	if err != nil {
		a.mediaFailed("无法创建 MediaMTX 临时目录")
		return
	}
	defer os.RemoveAll(dir)
	api, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		a.mediaFailed("无法分配 MediaMTX API 端口")
		return
	}
	apiAddr := api.Addr().String()
	api.Close()
	conf := filepath.Join(dir, "mediamtx.yml")
	if err := writeMediaConfig(conf, apiAddr, a.opts.rtmpAddr, executable); err != nil {
		a.mediaFailed("无法写入 MediaMTX 启动配置")
		return
	}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, a.opts.mediaPath, conf)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.Env = append(os.Environ(), hookEnv+"=http://"+hook.Addr().String())
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 4 * time.Second
		if err := cmd.Start(); err != nil {
			log.Printf("MediaMTX start: %v", err)
			a.mediaFailed("MediaMTX 无法启动，请检查接收程序路径")
		} else {
			done := make(chan struct{})
			go func() {
				if err := cmd.Wait(); err != nil && ctx.Err() == nil {
					log.Printf("MediaMTX stopped: %v", err)
				}
				close(done)
			}()
			m := &mediaClient{base: "http://" + apiAddr, rtmp: loopbackAddress(a.opts.rtmpAddr)}
			a.monitorMedia(ctx, m, signal, done)
			if ctx.Err() != nil {
				<-done
				return
			}
			a.mediaFailed("MediaMTX 已退出，请检查 RTMP 端口占用。服务将自动重试。")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *relay) monitorMedia(ctx context.Context, m *mediaClient, signal <-chan struct{}, done <-chan struct{}) {
	started := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-signal:
			timer.Stop()
		case <-timer.C:
		}
		ready, pending := a.reconcileMedia(ctx, m)
		switch {
		case !ready:
			if time.Since(started) > 4*time.Second && ctx.Err() == nil {
				a.mediaFailed("MediaMTX 管理接口不可用，请检查接收服务")
				// The child is still running. Its path config is reconciled when it responds.
			}
			timer.Reset(500 * time.Millisecond)
		case pending:
			timer.Reset(time.Second)
		default:
			timer.Reset(15 * time.Second)
		}
	}
}

func writeMediaConfig(path, apiAddr, rtmpAddr, executable string) error {
	hook := strconv.Quote(executable) + " media-event"
	data, err := yaml.Marshal(map[string]any{
		"logLevel": "error", "rtsp": false, "hls": false, "webrtc": false,
		"srt": false, "moq": false, "rtmp": true, "rtmpAddress": rtmpAddr,
		"api": true, "apiAddress": apiAddr,
		"authInternalUsers": []any{
			map[string]any{"user": "any", "ips": []string{"127.0.0.1", "::1"},
				"permissions": []any{map[string]string{"action": "api"}, map[string]string{"action": "read"}, map[string]string{"action": "publish", "path": "~^app/[A-Za-z0-9_.~/-]+$"}}},
			map[string]any{"user": "any", "ips": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "fc00::/7", "fe80::/10"}, "permissions": []any{
				map[string]string{"action": "publish", "path": "~^app/[A-Za-z0-9_.~/-]+$"},
				map[string]string{"action": "read", "path": "~^[A-Za-z0-9_-]{1,48}$"},
			}},
		},
		"pathDefaults": map[string]any{"runOnOnline": hook, "runOnOffline": hook},
		"paths": map[string]any{"~^app/[A-Za-z0-9_.~/-]+$": map[string]any{
			"source": "publisher", "overridePublisher": false,
		}},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func mediaEvent() error {
	target := os.Getenv(hookEnv)
	if target == "" {
		return errors.New("missing media event endpoint")
	}
	resp, err := mediaHTTP.Post(target, "application/octet-stream", nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}
