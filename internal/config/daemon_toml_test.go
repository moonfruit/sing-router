package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleTOML = `
[runtime]
sing_box_binary = "bin/sing-box"
config_dir      = "config.d"
ui_dir          = "ui"

[http]
listen = "127.0.0.1:9998"

[log]
level         = "debug"
file          = "log/sing-router.log"
rotate        = "internal"
max_size_mb   = 5
max_backups   = 3
disable_color = false

[zoo]
extract_keys              = ["outbounds", "route.rules", "route.rule_set", "route.final"]
rule_set_dedup_strategy   = "builtin_wins"
outbound_collision_action = "reject"

[download]
mirror_prefix          = ""
sing_box_url_template  = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"
sing_box_default_version = "latest"
cn_list_url            = "https://example.com/cn.txt"
http_timeout_seconds   = 60
http_retries           = 3

[install]
download_sing_box  = true
download_cn_list   = true
download_zashboard = false
auto_start         = false
`

func TestLoadDaemonConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(path, []byte(sampleTOML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.SingBoxBinary != "bin/sing-box" {
		t.Fatal("SingBoxBinary mismatch")
	}
	if cfg.HTTP.Listen != "127.0.0.1:9998" {
		t.Fatal("HTTP.Listen mismatch")
	}
	if cfg.Log.Level != "debug" {
		t.Fatal("Log.Level mismatch")
	}
	if cfg.Log.MaxSizeMB != 5 {
		t.Fatal("Log.MaxSizeMB mismatch")
	}
	if cfg.Zoo.RuleSetDedupStrategy != "builtin_wins" {
		t.Fatal("Zoo.RuleSetDedupStrategy mismatch")
	}
	if cfg.Install.DownloadSingBox != true {
		t.Fatal("Install.DownloadSingBox mismatch")
	}
}

func TestLoadDaemonConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	// 空文件 → 应得到全默认
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.SingBoxBinary != "bin/sing-box" {
		t.Fatalf("default SingBoxBinary mismatch: %q", cfg.Runtime.SingBoxBinary)
	}
	if cfg.HTTP.Listen != "127.0.0.1:9998" {
		t.Fatal("default HTTP.Listen mismatch")
	}
	if cfg.Log.Level != "info" {
		t.Fatal("default Log.Level mismatch")
	}
	if cfg.Log.MaxSizeMB != 10 {
		t.Fatal("default Log.MaxSizeMB mismatch")
	}
	if cfg.Log.MaxBackups != 5 {
		t.Fatal("default Log.MaxBackups mismatch")
	}
	if cfg.Zoo.RuleSetDedupStrategy != "builtin_wins" {
		t.Fatal("default RuleSetDedupStrategy mismatch")
	}
	if cfg.Download.HTTPTimeoutSeconds != 60 {
		t.Fatal("default http_timeout mismatch")
	}
	if cfg.Download.CNListURL == "" {
		t.Fatal("default cn_list_url should not be empty")
	}
}

func TestLoadDaemonConfigMissingFile(t *testing.T) {
	cfg, err := LoadDaemonConfig("/nonexistent/path/daemon.toml")
	if err != nil {
		t.Fatalf("missing file should default-load, got err: %v", err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:9998" {
		t.Fatal("missing file: defaults expected")
	}
}

func TestInstallFirmwareDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	body := "[install]\nfirmware = \"koolshare\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
}

func TestWriteInstallFirmware_AddsKeyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[runtime]\nui_dir = \"ui\"\n\n[install]\ndownload_sing_box = true\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
	if !cfg.Install.DownloadSingBox {
		t.Errorf("existing key DownloadSingBox lost")
	}
}

func TestWriteInstallFirmware_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[install]\nfirmware = \"merlin\"\ndownload_cn_list = true\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadDaemonConfig(path)
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
	if !cfg.Install.DownloadCNList {
		t.Errorf("existing key DownloadCNList lost")
	}
}

func TestWriteInstallFirmware_NoSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[runtime]\nui_dir = \"ui\"\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadDaemonConfig(path)
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
}
