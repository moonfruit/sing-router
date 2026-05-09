// Package config 包含 sing-router 的配置加载与 zoo.json 预处理。
package config

import (
	"errors"
	"os"

	"github.com/BurntSushi/toml"
)

// DefaultCNListURL 是 daemon.toml 缺省时的 cn.txt 拉取地址。
const DefaultCNListURL = "https://cdn.jsdelivr.net/gh/juewuy/ShellCrash@update/bin/geodata/china_ip_list.txt"

// envGiteeToken 是允许覆盖 [gitee].token 的环境变量名。
const envGiteeToken = "SING_ROUTER_GITEE_TOKEN"

// DaemonConfig 反映 daemon.toml 的全部字段；未来 B/C/E/F 模块各自加自己的 section。
type DaemonConfig struct {
	Runtime    RuntimeConfig    `toml:"runtime"`
	HTTP       HTTPConfig       `toml:"http"`
	Log        LogConfig        `toml:"log"`
	Supervisor SupervisorConfig `toml:"supervisor"`
	Zoo        ZooConfig        `toml:"zoo"`
	Download   DownloadConfig   `toml:"download"`
	Gitee      GiteeConfig      `toml:"gitee"`
	Sync       SyncConfig       `toml:"sync"`
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

// DownloadConfig 仅保留与 cn.txt（公网）相关的下载配置。sing-box 与 zoo 改走
// gitee 私仓（见 [gitee] 节）；阶段 B 之前的 mirror_prefix / sing_box_url_template /
// sing_box_default_version 已删除。
type DownloadConfig struct {
	CNListURL          string `toml:"cn_list_url"`
	HTTPTimeoutSeconds int    `toml:"http_timeout_seconds"`
	HTTPRetries        int    `toml:"http_retries"`
}

// GiteeConfig 描述访问 gitee 私有仓库所需的全局凭证与仓库定位。
// 每个资源类型（sing-box / zoo）在子节中各自指定 ref；rule_set 反向代理的
// ref 由 dns.json 中的 URL path 直接携带，不在此预声明。
type GiteeConfig struct {
	Token   string             `toml:"token"`
	Owner   string             `toml:"owner"`
	Repo    string             `toml:"repo"`
	SingBox GiteeSingBoxConfig `toml:"sing_box"`
	Zoo     GiteeZooConfig     `toml:"zoo"`
}

// GiteeSingBoxConfig 描述 sing-box 二进制在 gitee 仓库中的位置。
type GiteeSingBoxConfig struct {
	Ref                 string `toml:"ref"`                   // e.g. "binary"
	VersionPath         string `toml:"version_path"`          // e.g. "version.txt"
	TarballPathTemplate string `toml:"tarball_path_template"` // e.g. "sing-box-{version}-linux-arm64-musl.tar.gz"
}

// GiteeZooConfig 描述 zoo（config.json）在 gitee 仓库中的位置。
type GiteeZooConfig struct {
	Ref  string `toml:"ref"`  // e.g. "main"
	Path string `toml:"path"` // e.g. "config.json"
}

// SyncConfig 控制 daemon 后台周期同步行为。CLI 手动 update 命令不受此控制。
//
// 用 *int 区分"未提供"（应用默认）与"显式 0"（禁用）。
type SyncConfig struct {
	IntervalSeconds *int `toml:"interval_seconds"`   // 0 = 禁用 daemon 后台同步
	OnStartDelaySec *int `toml:"on_start_delay_sec"` // daemon 启动后多久首次同步
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
			applyDefaults(cfg)
			applyEnvOverrides(cfg)
			return cfg, nil
		}
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	applyEnvOverrides(cfg)
	return cfg, nil
}

// applyEnvOverrides 让运维场景 / CI 在不写 daemon.toml 的情况下注入敏感字段。
// 当前仅 SING_ROUTER_GITEE_TOKEN：非空时覆盖 [gitee].token。
func applyEnvOverrides(cfg *DaemonConfig) {
	if v := os.Getenv(envGiteeToken); v != "" {
		cfg.Gitee.Token = v
	}
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
			CNListURL:          DefaultCNListURL,
			HTTPTimeoutSeconds: 60,
			HTTPRetries:        3,
		},
		Gitee: GiteeConfig{
			Owner: "moonfruit",
			Repo:  "private",
			SingBox: GiteeSingBoxConfig{
				Ref:                 "binary",
				VersionPath:         "version.txt",
				TarballPathTemplate: "sing-box-{version}-linux-arm64-musl.tar.gz",
			},
			Zoo: GiteeZooConfig{
				Ref:  "main",
				Path: "config.json",
			},
		},
		Sync: SyncConfig{}, // applyDefaults 填补
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
	if cfg.Download.CNListURL == "" {
		cfg.Download.CNListURL = DefaultCNListURL
	}
	if cfg.Download.HTTPTimeoutSeconds == 0 {
		cfg.Download.HTTPTimeoutSeconds = 60
	}
	if cfg.Download.HTTPRetries == 0 {
		cfg.Download.HTTPRetries = 3
	}
	if cfg.Gitee.Owner == "" {
		cfg.Gitee.Owner = "moonfruit"
	}
	if cfg.Gitee.Repo == "" {
		cfg.Gitee.Repo = "private"
	}
	if cfg.Gitee.SingBox.Ref == "" {
		cfg.Gitee.SingBox.Ref = "binary"
	}
	if cfg.Gitee.SingBox.VersionPath == "" {
		cfg.Gitee.SingBox.VersionPath = "version.txt"
	}
	if cfg.Gitee.SingBox.TarballPathTemplate == "" {
		cfg.Gitee.SingBox.TarballPathTemplate = "sing-box-{version}-linux-arm64-musl.tar.gz"
	}
	if cfg.Gitee.Zoo.Ref == "" {
		cfg.Gitee.Zoo.Ref = "main"
	}
	if cfg.Gitee.Zoo.Path == "" {
		cfg.Gitee.Zoo.Path = "config.json"
	}
	if cfg.Sync.IntervalSeconds == nil {
		v := 21600
		cfg.Sync.IntervalSeconds = &v
	}
	if cfg.Sync.OnStartDelaySec == nil {
		v := 300
		cfg.Sync.OnStartDelaySec = &v
	}
}

// SyncIntervalSeconds 是 SyncConfig.IntervalSeconds 的安全访问器。未设置时回退默认值。
func (c SyncConfig) SyncIntervalSeconds() int {
	if c.IntervalSeconds == nil {
		return 21600
	}
	return *c.IntervalSeconds
}

// SyncOnStartDelaySec 是 SyncConfig.OnStartDelaySec 的安全访问器。
func (c SyncConfig) SyncOnStartDelaySec() int {
	if c.OnStartDelaySec == nil {
		return 300
	}
	return *c.OnStartDelaySec
}
