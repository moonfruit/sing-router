// Package daemon 包含 supervisor、状态机、HTTP API 与子进程编排。
package daemon

import (
	"fmt"
	"sync"
)

// State 是 daemon 状态机的枚举。
type State int

const (
	StateBooting State = iota
	StateRunning
	StateReloading
	StateDegraded
	StateStopping
	StateStopped
	StateFatal
)

func (s State) String() string {
	switch s {
	case StateBooting:
		return "booting"
	case StateRunning:
		return "running"
	case StateReloading:
		return "reloading"
	case StateDegraded:
		return "degraded"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// StateMachine 串行化状态转移；不内含异步行为。
type StateMachine struct {
	mu      sync.Mutex
	current State
}

// NewStateMachine 初始 booting。
func NewStateMachine() *StateMachine { return &StateMachine{current: StateBooting} }

// Current 返回当前 state。
func (s *StateMachine) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// allowed 描述合法的 (from→to) 关系。
//
// StateFatal → StateReloading 仅供 Supervisor.RecoverFromFailedApply 使用 ——
// Applier 在 auto-apply 流程中失败 revert 旧文件后,需要让 sing-box 用回旧 config
// 起来。不要把这条转移暴露到 HTTP API 或其它代码路径,以免破坏 Fatal 终态的语义。
var allowed = map[State]map[State]bool{
	StateBooting:   {StateRunning: true, StateFatal: true, StateStopping: true},
	StateRunning:   {StateReloading: true, StateDegraded: true, StateStopping: true},
	StateReloading: {StateRunning: true, StateFatal: true, StateStopping: true, StateReloading: true /* RecoverFromFailedApply 在 reloading 中失败仍允许再次 reload */},
	StateDegraded:  {StateRunning: true, StateStopping: true},
	StateStopping:  {StateStopped: true, StateFatal: true, StateBooting: true /* shutdown 中途取消极少见，但保留可能 */},
	StateStopped:   {StateBooting: true, StateStopping: true},
	StateFatal:     {StateStopping: true, StateReloading: true /* 仅 Applier revert 后用 */},
}

// Transition 切换状态；非法转移返回 error，不改变当前 state。
func (s *StateMachine) Transition(to State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	transitions, ok := allowed[s.current]
	if !ok {
		return fmt.Errorf("no transitions from %v", s.current)
	}
	if !transitions[to] {
		return fmt.Errorf("illegal transition %v → %v", s.current, to)
	}
	s.current = to
	return nil
}
