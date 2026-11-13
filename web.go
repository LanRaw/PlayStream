package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed web/*
var assets embed.FS

func (a *relay) handler(password string) http.Handler {
	files, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(files))
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		s, _ := a.snapshot()
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("GET /api/events", a.serveEvents)
	mux.HandleFunc("POST /api/address/refresh", a.refreshFromWeb)
	mux.HandleFunc("PUT /api/config", a.saveFromWeb)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		s, _ := a.snapshot()
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", "attachment; filename=config.yaml")
		yaml.NewEncoder(w).Encode(s.Config)
	})
	mux.HandleFunc("POST /api/config/import", a.importConfig)
	mux.HandleFunc("POST /api/diagnose", func(w http.ResponseWriter, r *http.Request) {
		a.startTest()
		w.WriteHeader(http.StatusNoContent)
	})

	expected := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		if password != "" {
			user, pass, ok := r.BasicAuth()
			got := sha256.Sum256([]byte(pass))
			if !ok || user != "driver" || subtle.ConstantTimeCompare(expected[:], got[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="PlayStream", charset="UTF-8"`)
				http.Error(w, "请使用 driver 登录", http.StatusUnauthorized)
				return
			}
		}
		if r.Method == "POST" || r.Method == "PUT" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, "请求来源与管理页不一致", http.StatusForbidden)
					return
				}
			}
			kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if kind != "application/json" && kind != "application/yaml" {
				http.Error(w, "不支持的请求格式", http.StatusUnsupportedMediaType)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *relay) refreshFromWeb(w http.ResponseWriter, r *http.Request) {
	if a.opts.bridge {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		s, _ := a.snapshot()
		if lanIP(host) && host != s.Address {
			s.Config.DNS.LANIP = host
			a.applyConfig(w, s.Revision, s.Config)
			return
		}
	} else {
		a.refreshAddress()
	}
	s, _ := a.snapshot()
	writeJSON(w, http.StatusOK, s)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

func sendError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (a *relay) saveFromWeb(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Revision uint64 `json:"revision"`
		Config   config `json:"config"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		sendError(w, http.StatusBadRequest, errors.New("配置格式无效"))
		return
	}
	a.applyConfig(w, req.Revision, req.Config)
}

func (a *relay) importConfig(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.ParseUint(r.Header.Get("If-Match"), 10, 64)
	if err != nil {
		sendError(w, http.StatusBadRequest, errors.New("缺少配置版本"))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		sendError(w, http.StatusBadRequest, errors.New("读取配置失败，文件不能超过 64 KB"))
		return
	}
	cfg, err := parseConfig(data)
	if err != nil {
		sendError(w, http.StatusBadRequest, err)
		return
	}
	a.applyConfig(w, rev, cfg)
}

func (a *relay) applyConfig(w http.ResponseWriter, rev uint64, cfg config) {
	err := a.update(rev, cfg)
	switch {
	case errors.Is(err, errStale):
		sendError(w, http.StatusConflict, err)
	case err != nil:
		sendError(w, http.StatusBadRequest, err)
	default:
		s, _ := a.snapshot()
		writeJSON(w, http.StatusOK, s)
	}
}

func (a *relay) serveEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	controller := http.NewResponseController(w)
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		s, changed := a.snapshot()
		payload, err := json.Marshal(s)
		if err != nil {
			return
		}
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil || controller.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-keepAlive.C:
		}
	}
}
