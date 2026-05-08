// Package config 包含 sing-router 的配置加载与 zoo.json 预处理。
package config

import (
	"errors"
	"os"

	"github.com/BurntSushi/toml"
)

// DefaultCNListURL 是 daemon.toml 缺省时的 cn.txt 拉取地址。
const DefaultCNListURL = "https://cdn.jsdelivr.net/gh/juewuy/ShellCrash@update/bin/geodata/china_ip_list.txt"

// DefaultSingBoxURLTemplate 是 sing-box 二进制下载模板。
const DefaultSingBoxURLTemplate = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"

// DaemonConfig 反映 daemon.toml 的全部字段；未来 B/C/E/F 模块各自加自己的 section。
type DaemonConfig struct {
	Runtime    RuntimeConfig    `toml:"runtime"`
	HTTP       HTTPConfig       `toml:"http"`
	Log        LogConfig        `toml:"log"`
	Supervisor SupervisorConfig `toml:"supervisor"`
	Zoo        ZooConfig        `toml:"zoo"`
	Download   DownloadConfig   `toml:"download"`
	Router     RouterConfig     `toml:"router"`
	Install    InstallConfig    `toml:"install"`
}

type RuntimeConfig struct {
	Rundir        string `toml:"rundir"`
	SingBoxBinary string `toml:"sing_box_binary"`
	ConfigDir     string `toml:"config_dir"`
	UIDir         string `toml:"ui_dir"`
}

type HTTPConfig struct {
	Listen string `toml:"listen"`
	Token  string `toml:"token"`
}

type LogConfig struct {
	Level        string `toml:"level"`
	File         string `toml:"file"`
	Rotate       string `toml:"rotate"`
	MaxSizeMB    int    `toml:"max_size_mb"`
	MaxBackups   int    `toml:"max_backups"`
	DisableColor bool   `toml:"disable_color"`
	IncludeStack bool   `toml:"include_stack"`
}

type SupervisorConfig struct {
	ReadyCheckDialInbounds      *bool  `toml:"ready_check_dial_inbounds"`
	ReadyCheckClashAPI          *bool  `toml:"ready_check_clash_api"`
	ReadyCheckTimeoutMs         *int   `toml:"ready_check_timeout_ms"`
	ReadyCheckIntervalMs        *int   `toml:"ready_check_interval_ms"`
	CrashPreReadyAction         string `toml:"crash_pre_ready_action"`
	CrashPostReadyBackoffMs     []int  `toml:"crash_post_ready_backoff_ms"`
	IptablesKeepWhenBackoffLtMs *int   `toml:"iptables_keep_when_backoff_lt_ms"`
	StopGraceSeconds            *int   `toml:"stop_grace_seconds"`
}

type ZooConfig struct {
	ExtractKeys             []string `toml:"extract_keys"`
	RuleSetDedupStrategy    string   `toml:"rule_set_dedup_strategy"`
	OutboundCollisionAction string   `toml:"outbound_collision_action"`
}

type DownloadConfig struct {
	MirrorPrefix          string `toml:"mirror_prefix"`
	SingBoxURLTemplate    string `toml:"sing_box_url_template"`
	SingBoxDefaultVersion string `toml:"sing_box_default_version"`
	CNListURL             string `toml:"cn_list_url"`
	HTTPTimeoutSeconds    int    `toml:"http_timeout_seconds"`
	HTTPRetries           int    `toml:"http_retries"`
}

type RouterConfig struct {
	DnsPort      *int    `toml:"dns_port"`
	RedirectPort *int    `toml:"redirect_port"`
	RouteMark    *string `toml:"route_mark"`
	BypassMark   *string `toml:"bypass_mark"`
	Tun          *string `toml:"tun"`
	FakeIP       *string `toml:"fakeip"`
	LAN          *string `toml:"lan"`
	RouteTable   *int    `toml:"route_table"`
	ProxyPorts   *string `toml:"proxy_ports"`
}

type InstallConfig struct {
	DownloadSingBox   bool   `toml:"download_sing_box"`
	DownloadCNList    bool   `toml:"download_cn_list"`
	DownloadZashboard bool   `toml:"download_zashboard"`
	AutoStart         bool   `toml:"auto_start"`
	Firmware          string `toml:"firmware"` // "koolshare" | "merlin" | ""
}

// LoadDaemonConfig 从给定路径加载 daemon.toml。文件不存在时返回全默认 config，
// 不报错（首次 install 之前 status 命令也能用）。
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	return cfg, nil
}

func defaultConfig() *DaemonConfig {
	cfg := &DaemonConfig{
		Runtime: RuntimeConfig{
			SingBoxBinary: "bin/sing-box",
			ConfigDir:     "config.d",
			UIDir:         "ui",
		},
		HTTP: HTTPConfig{Listen: "127.0.0.1:9998"},
		Log: LogConfig{
			Level:      "info",
			File:       "log/sing-router.log",
			Rotate:     "internal",
			MaxSizeMB:  10,
			MaxBackups: 5,
		},
		Zoo: ZooConfig{
			ExtractKeys:             []string{"outbounds", "route.rules", "route.rule_set", "route.final"},
			RuleSetDedupStrategy:    "builtin_wins",
			OutboundCollisionAction: "reject",
		},
		Download: DownloadConfig{
			SingBoxURLTemplate:    DefaultSingBoxURLTemplate,
			SingBoxDefaultVersion: "latest",
			CNListURL:             DefaultCNListURL,
			HTTPTimeoutSeconds:    60,
			HTTPRetries:           3,
		},
		Install: InstallConfig{
			DownloadSingBox:   false,
			DownloadCNList:    false,
			DownloadZashboard: false,
			AutoStart:         false,
		},
	}
	return cfg
}

// applyDefaults 在解码后填补未提供的字段。
func applyDefaults(cfg *DaemonConfig) {
	if cfg.Runtime.SingBoxBinary == "" {
		cfg.Runtime.SingBoxBinary = "bin/sing-box"
	}
	if cfg.Runtime.ConfigDir == "" {
		cfg.Runtime.ConfigDir = "config.d"
	}
	if cfg.Runtime.UIDir == "" {
		cfg.Runtime.UIDir = "ui"
	}
	if cfg.HTTP.Listen == "" {
		cfg.HTTP.Listen = "127.0.0.1:9998"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.File == "" {
		cfg.Log.File = "log/sing-router.log"
	}
	if cfg.Log.Rotate == "" {
		cfg.Log.Rotate = "internal"
	}
	if cfg.Log.MaxSizeMB == 0 {
		cfg.Log.MaxSizeMB = 10
	}
	if cfg.Log.MaxBackups == 0 {
		cfg.Log.MaxBackups = 5
	}
	if len(cfg.Zoo.ExtractKeys) == 0 {
		cfg.Zoo.ExtractKeys = []string{"outbounds", "route.rules", "route.rule_set", "route.final"}
	}
	if cfg.Zoo.RuleSetDedupStrategy == "" {
		cfg.Zoo.RuleSetDedupStrategy = "builtin_wins"
	}
	if cfg.Zoo.OutboundCollisionAction == "" {
		cfg.Zoo.OutboundCollisionAction = "reject"
	}
	if cfg.Download.SingBoxURLTemplate == "" {
		cfg.Download.SingBoxURLTemplate = DefaultSingBoxURLTemplate
	}
	if cfg.Download.SingBoxDefaultVersion == "" {
		cfg.Download.SingBoxDefaultVersion = "latest"
	}
	if cfg.Download.CNListURL == "" {
		cfg.Download.CNListURL = DefaultCNListURL
	}
	if cfg.Download.HTTPTimeoutSeconds == 0 {
		cfg.Download.HTTPTimeoutSeconds = 60
	}
	if cfg.Download.HTTPRetries == 0 {
		cfg.Download.HTTPRetries = 3
	}
}
