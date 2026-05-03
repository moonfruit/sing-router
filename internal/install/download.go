package install

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RenderURL 拼接 mirror_prefix + 已渲染 {version} 的 raw URL。
func RenderURL(mirrorPrefix, template, version string) string {
	raw := strings.ReplaceAll(template, "{version}", version)
	if mirrorPrefix == "" {
		return raw
	}
	if !strings.HasSuffix(mirrorPrefix, "/") {
		mirrorPrefix += "/"
	}
	return mirrorPrefix + raw
}

// DownloadFile 把 url 下载到 target；带原子写、超时与重试。
// timeoutSec：单次请求超时；retries：失败后重试次数（首次不计）。
func DownloadFile(url, target string, timeoutSec, retries int) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if retries < 0 {
		retries = 0
	}
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := downloadOnce(client, url, target); err != nil {
			lastErr = err
			if attempt < retries {
				time.Sleep(time.Duration(attempt+1) * time.Second)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("download %s after %d attempts: %w", url, retries+1, lastErr)
}

func downloadOnce(client *http.Client, url, target string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
