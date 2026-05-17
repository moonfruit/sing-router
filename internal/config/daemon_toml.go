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

// envSeqURL / envSeqAPIKey 是允许覆盖 [seq] 节敏感字段的环境变量名。
const (
	envSeqURL    = "SING_ROUTER_SEQ_URL"
	envSeqAPIKey = "SING_ROUTER_SEQ_API_KEY"
)

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
	Seq        SeqConfig        `toml:"seq"`
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
	ColorProfile string `toml:"color_profile"` // auto | truecolor | 256 | 8 | none
	IncludeStack bool   `toml:"include_stack"`
}

type SupervisorConfig struct {
	ReadyCheckDialInbounds  *bool  `toml:"ready_check_dial_inbounds"`
	ReadyCheckClashAPI      *bool  `toml:"ready_check_clash_api"`
	ReadyCheckTimeoutMs     *int   `toml:"ready_check_timeout_ms"`
	ReadyCheckIntervalMs    *int   `toml:"ready_check_interval_ms"`
	CrashPreReadyAction     string `toml:"crash_pre_ready_action"`
	CrashPostReadyBackoffMs []int  `toml:"crash_post_ready_backoff_ms"`
	StopGraceSeconds        *int   `toml:"stop_grace_seconds"`
	RouteWatchIntervalSec   *int   `toml:"route_watch_interval_sec"`
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
// 用 *int / *bool 区分"未提供"（应用默认）与"显式 false / 0"。
type SyncConfig struct {
	IntervalSeconds *int  `toml:"interval_seconds"`   // 0 = 禁用 daemon 后台同步
	OnStartDelaySec *int  `toml:"on_start_delay_sec"` // daemon 启动后多久首次同步
	AutoApply       *bool `toml:"auto_apply"`         // 默认 true：拉到新资源后自动 apply（zoo/sing-box → restart；cn.txt → ipset reload）。false 则仅 log。
}

// SeqConfig 控制把 CLEF 事件推送到远程 Seq 服务的 sink。默认完全关闭——
// Enabled=false 或 URL="" 时 daemon 不构造 sink、不订阅 bus，与从前行为一致。
//
// 启用 = Enabled=true && URL!=""；不满足时 daemon 启动会发一条 seq.disabled
// 事件后继续运行（不退出）。Source 字段：sink 自身诊断（buffer_overflow /
// post_failed / shutdown_post_failed）走独立 emitter 的 Source="sing2seq"，
// 与上游 sing2seq CLI 一致；daemon 业务事件 Source="daemon"，sing-box stderr
// 解析事件 Source="sing-box"。Seq 端按 Source 切视图。
type SeqConfig struct {
	Enabled  bool   `toml:"enabled"`
	URL      string `toml:"url"`
	APIKey   string `toml:"api_key"`
	Insecure bool   `toml:"insecure"`
	// Level 按事件 @l 字段过滤；trace/debug/info/warn/error/fatal；
	// 空字符串走默认 info。非法值在 wireup 阶段 fallback 到 info 并告警。
	// 与 [log].level 各自独立——支持"本地详细 / 远程精简"或反过来。
	Level string `toml:"level"`
	// 高级调优；nil/0 走 seq.NewSink 内部默认（与 sing2seq CLI 一致）。
	BatchSize      *int `toml:"batch_size"`
	ChannelBuffer  *int `toml:"channel_buffer"`
	MaxPending     *int `toml:"max_pending"`
	DropTarget     *int `toml:"drop_target"`
	InitialBackoff *int `toml:"initial_backoff_ms"`
	MaxBackoff     *int `toml:"max_backoff_ms"`
	// CloseDrainTimeoutSec 是 daemon shutdown 时给 sink.Close 同步 drain 的总
	// 超时秒数；超时后放弃 pending（sink 内部 shutdown_post_failed 已处理），
	// 让 daemon 退出不被卡死。nil/0 走默认 10s。
	CloseDrainTimeoutSec *int `toml:"close_drain_timeout_seconds"`
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
// SING_ROUTER_GITEE_TOKEN 覆盖 [gitee].token；SING_ROUTER_SEQ_URL /
// SING_ROUTER_SEQ_API_KEY 覆盖 [seq] 的相应字段（仅覆盖值，Enabled 仍由
// toml 决定，避免 env 误启用 sink）。
func applyEnvOverrides(cfg *DaemonConfig) {
	if v := os.Getenv(envGiteeToken); v != "" {
		cfg.Gitee.Token = v
	}
	if v := os.Getenv(envSeqURL); v != "" {
		cfg.Seq.URL = v
	}
	if v := os.Getenv(envSeqAPIKey); v != "" {
		cfg.Seq.APIKey = v
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
	if cfg.Log.ColorProfile == "" {
		cfg.Log.ColorProfile = "auto"
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
	if cfg.Sync.AutoApply == nil {
		v := true
		cfg.Sync.AutoApply = &v
	}
	if cfg.Seq.Level == "" {
		cfg.Seq.Level = "info"
	}
	if cfg.Seq.CloseDrainTimeoutSec == nil {
		v := 10
		cfg.Seq.CloseDrainTimeoutSec = &v
	}
}

// SeqCloseDrainTimeoutSeconds 是 CloseDrainTimeoutSec 的安全访问器。未设置回退 10s。
func (c SeqConfig) SeqCloseDrainTimeoutSeconds() int {
	if c.CloseDrainTimeoutSec == nil || *c.CloseDrainTimeoutSec <= 0 {
		return 10
	}
	return *c.CloseDrainTimeoutSec
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

// SyncAutoApply 是 SyncConfig.AutoApply 的安全访问器。未设置时默认 true。
func (c SyncConfig) SyncAutoApply() bool {
	if c.AutoApply == nil {
		return true
	}
	return *c.AutoApply
}
