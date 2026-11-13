package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

type stream struct {
	ID      string `yaml:"id" json:"id"`
	Name    string `yaml:"name" json:"name"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type networkConfig struct {
	Upstream  string `yaml:"upstream,omitempty" json:"upstream"`
	LANIP     string `yaml:"lan_ip,omitempty" json:"lanIP"`
	Interface string `yaml:"interface,omitempty" json:"interface"`
}

type config struct {
	Version int           `yaml:"version" json:"version"`
	DNS     networkConfig `yaml:"dns" json:"dns"`
	Streams []stream      `yaml:"streams" json:"streams"`
	Active  string        `yaml:"active_stream" json:"active"`
}

var aliasPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

func lanIP(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func parseConfig(data []byte) (config, error) {
	var cfg config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("配置文件只能包含一个 YAML 文档")
	}
	return cfg, cfg.validate()
}

func (cfg config) validate() error {
	if cfg.Version != 1 {
		return errors.New("配置版本必须为 1")
	}
	upstream, err := netip.ParseAddr(cfg.DNS.Upstream)
	if cfg.DNS.Upstream != "" && (err != nil || upstream.IsUnspecified() || upstream.IsMulticast()) {
		return errors.New("上游 DNS 留空时自动配置，也可填写有效的 IP 地址")
	}
	if cfg.DNS.LANIP != "" && !lanIP(cfg.DNS.LANIP) {
		return errors.New("接收器地址必须是局域网 IPv4 地址")
	}
	if cfg.DNS.LANIP != "" && cfg.DNS.LANIP == cfg.DNS.Upstream {
		return errors.New("上游 DNS 不能指向接收器自身")
	}
	if len(cfg.Streams) > 64 {
		return errors.New("最多保存 64 个直播对象")
	}
	seen := make(map[string]bool, len(cfg.Streams))
	active := cfg.Active == ""
	for _, s := range cfg.Streams {
		if !aliasPattern.MatchString(s.ID) || s.ID == "all_others" {
			return fmt.Errorf("别名 %q 无效，请使用 1–48 个英文字母、数字、下划线或短横线", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("别名 %q 已存在", s.ID)
		}
		if strings.TrimSpace(s.Name) == "" || utf8.RuneCountInString(s.Name) > 80 {
			return errors.New("请输入 1–80 个字符的显示名称")
		}
		seen[s.ID] = true
		active = active || (s.ID == cfg.Active && s.Enabled)
	}
	if !active {
		return errors.New("当前直播对象必须存在且已启用")
	}
	return nil
}

func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := config{Version: 1, Streams: []stream{}}
		return cfg, saveConfig(path, cfg)
	}
	if err != nil {
		return config{}, err
	}
	return parseConfig(data)
}

func saveConfig(path string, cfg config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := atomicWrite(path+".bak", old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, data)
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
