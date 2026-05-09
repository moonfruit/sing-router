package sync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

// fakeGiteeServer 模拟 gitee 私仓的 raw 接口。请求映射到 routes 上，命中即返回；
// 未命中返回 404。每个 route 都会被注入 ETag = "v1"，以便测试 etag 路径。
type fakeRoute struct {
	body       []byte
	etag       string
	statusCode int // 默认 200；非 0 时直接返回
}

func newFakeGitee(routes map[string]*fakeRoute) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	hits := &atomic.Int32{}
	notMod := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 路径形如 /repos/moonfruit/private/raw/{key}（注：APIBase 已被替换为 srv.URL，
		// 不带 /api/v5 前缀；客户端在其后拼 /repos/...）
		const prefix = "/repos/moonfruit/private/raw/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		route, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if route.statusCode != 0 {
			http.Error(w, "fake-error", route.statusCode)
			return
		}
		if route.etag != "" {
			if r.Header.Get("If-None-Match") == route.etag {
				notMod.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", route.etag)
		}
		_, _ = w.Write(route.body)
	}))
	return srv, hits, notMod
}

func makeFakeSingBoxTarball(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	payload := []byte(body)
	hdr := &tar.Header{
		Name:     "sing-box-1.13.5-linux-arm64-musl/sing-box",
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newTestUpdater(t *testing.T, apiBase string) (*Updater, string) {
	t.Helper()
	rundir := t.TempDir()
	cfg := &config.DaemonConfig{
		Gitee: config.GiteeConfig{
			Token: "tk",
			Owner: "moonfruit",
			Repo:  "private",
			SingBox: config.GiteeSingBoxConfig{
				Ref:                 "binary",
				VersionPath:         "version.txt",
				TarballPathTemplate: "sing-box-{version}-linux-arm64-musl.tar.gz",
			},
			Zoo: config.GiteeZooConfig{
				Ref:  "main",
				Path: "config.json",
			},
		},
		Download: config.DownloadConfig{
			CNListURL:          "PLACEHOLDER", // 测试单独覆盖
			HTTPRetries:        0,
			HTTPTimeoutSeconds: 30,
		},
	}
	u := NewUpdater(cfg, rundir)
	u.gitee.APIBase = apiBase
	return u, rundir
}

func TestUpdateSingBox_FreshDownloadsAndExtracts(t *testing.T) {
	tarball := makeFakeSingBoxTarball(t, "fake-singbox-binary")
	routes := map[string]*fakeRoute{
		"version.txt":                                 {body: []byte("1.13.5\n"), etag: `"v1"`},
		"sing-box-1.13.5-linux-arm64-musl.tar.gz":     {body: tarball, etag: `"t1"`},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	u, rundir := newTestUpdater(t, srv.URL)

	changed, version, err := u.UpdateSingBox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first call should be changed=true")
	}
	if version != "1.13.5" {
		t.Fatalf("version = %q", version)
	}
	binPath := filepath.Join(rundir, "bin", "sing-box")
	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-singbox-binary" {
		t.Fatalf("binary mismatch: %q", string(data))
	}
	st, _ := os.Stat(binPath)
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %v", st.Mode().Perm())
	}
}

func TestUpdateSingBox_SkipsExtractWhenETagHitAndBinExists(t *testing.T) {
	tarball := makeFakeSingBoxTarball(t, "v1-binary")
	routes := map[string]*fakeRoute{
		"version.txt":                             {body: []byte("1.13.5"), etag: `"v1"`},
		"sing-box-1.13.5-linux-arm64-musl.tar.gz": {body: tarball, etag: `"t1"`},
	}
	srv, _, notMod := newFakeGitee(routes)
	defer srv.Close()
	u, rundir := newTestUpdater(t, srv.URL)

	if _, _, err := u.UpdateSingBox(context.Background()); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(rundir, "bin", "sing-box")
	// 篡改本地 bin 标记，验证第二次调用不重写。
	if err := os.WriteFile(binPath, []byte("locally-modified"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, _, err := u.UpdateSingBox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("etag-hit + bin-exists should skip extract")
	}
	if data, _ := os.ReadFile(binPath); string(data) != "locally-modified" {
		t.Fatal("bin should not have been overwritten")
	}
	if notMod.Load() < 1 {
		t.Fatal("no 304 observed")
	}
}

func TestUpdateSingBox_ReextractsWhenBinMissing(t *testing.T) {
	tarball := makeFakeSingBoxTarball(t, "binary-content")
	routes := map[string]*fakeRoute{
		"version.txt":                             {body: []byte("1.13.5"), etag: `"v1"`},
		"sing-box-1.13.5-linux-arm64-musl.tar.gz": {body: tarball, etag: `"t1"`},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	u, rundir := newTestUpdater(t, srv.URL)
	if _, _, err := u.UpdateSingBox(context.Background()); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(rundir, "bin", "sing-box")
	if err := os.Remove(binPath); err != nil {
		t.Fatal(err)
	}
	changed, _, err := u.UpdateSingBox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("bin missing should force re-extract regardless of etag")
	}
	if data, _ := os.ReadFile(binPath); string(data) != "binary-content" {
		t.Fatal("bin not restored")
	}
}

func TestUpdateSingBox_TarballMissingBinaryReturnsError(t *testing.T) {
	// tarball 里没有 sing-box 文件。
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "junk/README", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi\n!"))
	_ = tw.Close()
	_ = gw.Close()

	routes := map[string]*fakeRoute{
		"version.txt":                             {body: []byte("1.13.5")},
		"sing-box-1.13.5-linux-arm64-musl.tar.gz": {body: buf.Bytes()},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	u, _ := newTestUpdater(t, srv.URL)
	_, _, err := u.UpdateSingBox(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sing-box binary not found") {
		t.Fatalf("expected missing-binary error, got %v", err)
	}
}

func TestUpdateSingBox_EmptyVersionFails(t *testing.T) {
	routes := map[string]*fakeRoute{
		"version.txt": {body: []byte("   \n  ")},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	u, _ := newTestUpdater(t, srv.URL)
	_, _, err := u.UpdateSingBox(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty sing-box version") {
		t.Fatalf("expected empty-version error, got %v", err)
	}
}

func TestUpdateZoo_WritesRawJSON(t *testing.T) {
	const zooBody = `{"outbounds":[]}`
	routes := map[string]*fakeRoute{
		"config.json": {body: []byte(zooBody), etag: `"z1"`},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	u, rundir := newTestUpdater(t, srv.URL)

	changed, err := u.UpdateZoo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first call should be changed=true")
	}
	got, _ := os.ReadFile(filepath.Join(rundir, "var", "zoo.raw.json"))
	if string(got) != zooBody {
		t.Fatalf("zoo content mismatch: %q", string(got))
	}
	// 第二次：etag 命中。
	changed, err = u.UpdateZoo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second call should hit etag")
	}
}

func TestUpdateCNList_FromPublicURL(t *testing.T) {
	const payload = "1.0.0.0/8\n2.0.0.0/8\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"cn1"`)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	rundir := t.TempDir()
	cfg := &config.DaemonConfig{
		Download: config.DownloadConfig{CNListURL: srv.URL, HTTPRetries: 0},
	}
	u := NewUpdater(cfg, rundir)

	changed, err := u.UpdateCNList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first call should be changed=true")
	}
	got, _ := os.ReadFile(filepath.Join(rundir, "var", "cn.txt"))
	if string(got) != payload {
		t.Fatalf("cn.txt content mismatch: %q", string(got))
	}
}

func TestUpdateCNList_EmptyURLFails(t *testing.T) {
	rundir := t.TempDir()
	cfg := &config.DaemonConfig{Download: config.DownloadConfig{CNListURL: ""}}
	u := NewUpdater(cfg, rundir)
	_, err := u.UpdateCNList(context.Background())
	if err == nil {
		t.Fatal("expected error on empty url")
	}
}

func TestUpdateAll_PartialFailureAggregates(t *testing.T) {
	tarball := makeFakeSingBoxTarball(t, "binary")
	// zoo 故意配 500 触发失败。
	routes := map[string]*fakeRoute{
		"version.txt":                             {body: []byte("1.13.5")},
		"sing-box-1.13.5-linux-arm64-musl.tar.gz": {body: tarball},
		"config.json":                             {statusCode: http.StatusInternalServerError},
	}
	srv, _, _ := newFakeGitee(routes)
	defer srv.Close()
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "cn-payload")
	}))
	defer cnSrv.Close()

	u, _ := newTestUpdater(t, srv.URL)
	u.cfg.Download.CNListURL = cnSrv.URL

	r := u.UpdateAll(context.Background())
	if r.SingBox.Err != nil {
		t.Fatalf("sing-box should succeed: %v", r.SingBox.Err)
	}
	if !r.SingBox.Changed {
		t.Fatal("sing-box should be changed")
	}
	if r.Zoo.Err == nil {
		t.Fatal("zoo should fail")
	}
	if r.CNList.Err != nil {
		t.Fatalf("cn-list should succeed: %v", r.CNList.Err)
	}
	if !r.HasError() {
		t.Fatal("HasError should be true with one failure")
	}
}
