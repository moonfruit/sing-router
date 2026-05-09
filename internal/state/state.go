// Package state 持久化 daemon 的运行时状态到 state.json。
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// State 是 daemon 持久化的最小状态。
type State struct {
	LastBootAt            string `json:"last_boot_at"`
	RestartCount          int    `json:"restart_count"`
	LastZooLoadedAt       string `json:"last_zoo_loaded_at"`
	LastIptablesAppliedAt string `json:"last_iptables_applied_at"`
}

// Load 读取 state.json；不存在返回空 State，不报错。
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, err
	}
	s := &State{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Save 原子写入 state.json：tmp 文件 + rename。
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// osStat 为测试与未来注入预留的间接层。
var osStat = os.Stat
