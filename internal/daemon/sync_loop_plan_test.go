package daemon

import (
	"errors"
	"slices"
	"testing"
	"time"

	syncpkg "github.com/moonfruit/sing-router/internal/sync"
)

func TestPlanApply(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	in := applyInputs{Tarball: "T2", ZooRaw: "Z1", CN: "C1"}
	applied := applyState{SingBoxTarball: "T2", ZooRaw: "Z1", CNTxt: "C1"}
	unchanged := syncpkg.AllResult{}

	cases := []struct {
		name      string
		r         syncpkg.AllResult
		st        applyState
		in        applyInputs
		wantKinds []Resource
		wantRetry []Resource
		wantSkip  bool
	}{
		{
			name: "all applied, nothing changed",
			r:    unchanged, st: applied, in: in,
		},
		{
			name: "changed resources pass through",
			r: syncpkg.AllResult{
				Zoo:    syncpkg.ItemResult{Changed: true},
				CNList: syncpkg.ItemResult{Changed: true},
			},
			st: applied, in: in,
			wantKinds: []Resource{ResourceZoo, ResourceCN},
		},
		{
			name:      "failed sing-box upgrade is retried although etag did not change",
			r:         unchanged,
			st:        applyState{SingBoxTarball: "T1", ZooRaw: "Z1", CNTxt: "C1"},
			in:        in,
			wantKinds: []Resource{ResourceSingBox},
			wantRetry: []Resource{ResourceSingBox},
		},
		{
			name:      "pending zoo and cn are retried",
			r:         unchanged,
			st:        applyState{SingBoxTarball: "T2"},
			in:        in,
			wantKinds: []Resource{ResourceZoo, ResourceCN},
			wantRetry: []Resource{ResourceZoo, ResourceCN},
		},
		{
			name: "download error suppresses that resource",
			r:    syncpkg.AllResult{SingBox: syncpkg.ItemResult{Err: errors.New("gitee down")}},
			st:   applyState{SingBoxTarball: "T1", ZooRaw: "Z1", CNTxt: "C1"},
			in:   in,
		},
		{
			name: "never downloaded is not pending",
			r:    unchanged,
			st:   applyState{},
			in:   applyInputs{},
		},
		{
			name: "same inputs failed recently: cool down",
			r:    unchanged,
			st: applyState{SingBoxTarball: "T1", ZooRaw: "Z1", CNTxt: "C1",
				LastFailure: &applyFailure{Fingerprint: in.fingerprint(), At: now.Add(-time.Hour)}},
			in:       in,
			wantSkip: true,
		},
		{
			name: "same inputs failed over a day ago: retry",
			r:    unchanged,
			st: applyState{SingBoxTarball: "T1", ZooRaw: "Z1", CNTxt: "C1",
				LastFailure: &applyFailure{Fingerprint: in.fingerprint(), At: now.Add(-25 * time.Hour)}},
			in:        in,
			wantKinds: []Resource{ResourceSingBox},
			wantRetry: []Resource{ResourceSingBox},
		},
		{
			name: "inputs changed since the failure: retry immediately",
			r:    syncpkg.AllResult{Zoo: syncpkg.ItemResult{Changed: true}},
			st: applyState{SingBoxTarball: "T1", ZooRaw: "Z0", CNTxt: "C1",
				LastFailure: &applyFailure{Fingerprint: "old", At: now.Add(-time.Hour)}},
			in:        in,
			wantKinds: []Resource{ResourceSingBox, ResourceZoo},
			wantRetry: []Resource{ResourceSingBox},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planApply(tc.r, tc.st, tc.in, now)
			if p.skip() != tc.wantSkip {
				t.Fatalf("skip = %v (until %v), want %v", p.skip(), p.CoolUntil, tc.wantSkip)
			}
			if tc.wantSkip {
				return
			}
			if !slices.Equal(p.Kinds, tc.wantKinds) {
				t.Errorf("kinds = %v, want %v", p.Kinds, tc.wantKinds)
			}
			if !slices.Equal(p.Retry, tc.wantRetry) {
				t.Errorf("retry = %v, want %v", p.Retry, tc.wantRetry)
			}
		})
	}
}

// 指纹必须随 sing-router 版本变化：修 bug 的新版本上线后应立即重试，不等冷却。
func TestApplyInputsFingerprintIncludesVersion(t *testing.T) {
	in := applyInputs{Tarball: "T", ZooRaw: "Z", CN: "C"}
	if in.fingerprintWith("v1") == in.fingerprintWith("v2") {
		t.Fatal("fingerprint must depend on sing-router version")
	}
	if in.fingerprintWith("v1") == (applyInputs{Tarball: "T2", ZooRaw: "Z", CN: "C"}).fingerprintWith("v1") {
		t.Fatal("fingerprint must depend on inputs")
	}
}
