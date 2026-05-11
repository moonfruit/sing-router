package daemon

import "testing"

func TestStateStrings(t *testing.T) {
	cases := map[State]string{
		StateBooting:   "booting",
		StateRunning:   "running",
		StateReloading: "reloading",
		StateDegraded:  "degraded",
		StateStopping:  "stopping",
		StateStopped:   "stopped",
		StateFatal:     "fatal",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Fatalf("%v: want %s got %s", s, want, s.String())
		}
	}
}

func TestStateMachineInitialBooting(t *testing.T) {
	sm := NewStateMachine()
	if sm.Current() != StateBooting {
		t.Fatalf("initial: %v", sm.Current())
	}
}

func TestStateMachineHappyPath(t *testing.T) {
	sm := NewStateMachine()
	must := func(err error) {
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	must(sm.Transition(StateRunning))
	must(sm.Transition(StateReloading))
	must(sm.Transition(StateRunning))
	must(sm.Transition(StateDegraded))
	must(sm.Transition(StateRunning))
	must(sm.Transition(StateStopping))
	must(sm.Transition(StateStopped))
	must(sm.Transition(StateBooting))
	must(sm.Transition(StateRunning))
}

func TestStateMachineRejectsIllegalTransitions(t *testing.T) {
	sm := NewStateMachine()
	// booting → stopped 非法（应先经 stopping）
	if err := sm.Transition(StateStopped); err == nil {
		t.Fatal("expected illegal transition error")
	}
}

func TestStateMachineFatalIsTerminal(t *testing.T) {
	sm := NewStateMachine()
	if err := sm.Transition(StateFatal); err != nil {
		t.Fatalf("booting→fatal should be ok: %v", err)
	}
	// fatal 之后只能 → stopping （SIGTERM/shutdown）;直接回 running 仍非法。
	if err := sm.Transition(StateRunning); err == nil {
		t.Fatal("fatal→running should be illegal")
	}
	if err := sm.Transition(StateStopping); err != nil {
		t.Fatalf("fatal→stopping should be ok: %v", err)
	}
}

// TestStateMachineFatalToReloadingForApplyRecover: fatal → reloading 允许,
// 仅供 Supervisor.RecoverFromFailedApply 使用,把 sing-box 用 revert 后的
// 旧配置重新拉起来。
func TestStateMachineFatalToReloadingForApplyRecover(t *testing.T) {
	sm := NewStateMachine()
	if err := sm.Transition(StateFatal); err != nil {
		t.Fatalf("booting→fatal: %v", err)
	}
	if err := sm.Transition(StateReloading); err != nil {
		t.Fatalf("fatal→reloading should be ok for apply-recover: %v", err)
	}
	if err := sm.Transition(StateRunning); err != nil {
		t.Fatalf("reloading→running: %v", err)
	}
}

// TestStateMachineReloadingToReloading: 允许 reloading→reloading 重新进入,
// RecoverFromFailedApply 在 Restart 失败后(可能仍处 reloading)也要能再走一遍。
func TestStateMachineReloadingToReloading(t *testing.T) {
	sm := NewStateMachine()
	if err := sm.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StateReloading); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StateReloading); err != nil {
		t.Fatalf("reloading→reloading should be ok: %v", err)
	}
}
