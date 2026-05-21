package bark

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os/exec"
	"testing"
)

// decrypt 是测试侧的逆运算，用于验证 encrypt 产出的密文确实可解回原文。
func decrypt(spec *cipherSpec, ciphertextB64, iv string) ([]byte, error) {
	block, err := aes.NewCipher(spec.key)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, err
	}
	switch spec.mode {
	case "CBC":
		out := make([]byte, len(ct))
		cipher.NewCBCDecrypter(block, []byte(iv)).CryptBlocks(out, ct)
		return pkcs7Unpad(out)
	case "ECB":
		out := make([]byte, len(ct))
		for i := 0; i < len(ct); i += aes.BlockSize {
			block.Decrypt(out[i:i+aes.BlockSize], ct[i:i+aes.BlockSize])
		}
		return pkcs7Unpad(out)
	case "GCM":
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		return gcm.Open(nil, []byte(iv), ct, nil)
	default:
		return nil, fmt.Errorf("bad mode %q", spec.mode)
	}
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > len(data) {
		return nil, fmt.Errorf("bad padding %d", pad)
	}
	return data[:len(data)-pad], nil
}

func keyOf(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}

func TestCipherRoundTrip(t *testing.T) {
	algos := map[string]int{"AES128": 16, "AES192": 24, "AES256": 32}
	modes := []string{"CBC", "ECB", "GCM"}
	payload := []byte(`{"title":"测试","body":"sing-box 已恢复"}`)

	for algo, klen := range algos {
		for _, mode := range modes {
			spec, err := newCipherSpec(algo, mode, []byte(keyOf(klen)))
			if err != nil {
				t.Fatalf("%s/%s: newCipherSpec: %v", algo, mode, err)
			}
			ct, iv, err := spec.encrypt(payload)
			if err != nil {
				t.Fatalf("%s/%s: encrypt: %v", algo, mode, err)
			}
			got, err := decrypt(spec, ct, iv)
			if err != nil {
				t.Fatalf("%s/%s: decrypt: %v", algo, mode, err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("%s/%s: round-trip mismatch: %q", algo, mode, got)
			}
		}
	}
}

func TestNewCipherSpecKeyLengthMismatch(t *testing.T) {
	if _, err := newCipherSpec("AES256", "CBC", []byte(keyOf(16))); err == nil {
		t.Error("AES256 with 16-byte key should fail")
	}
	if _, err := newCipherSpec("AES128", "CBC", []byte(keyOf(32))); err == nil {
		t.Error("AES128 with 32-byte key should fail")
	}
}

func TestNewCipherSpecBadParams(t *testing.T) {
	if _, err := newCipherSpec("AES512", "CBC", []byte(keyOf(16))); err == nil {
		t.Error("bogus algorithm should fail")
	}
	if _, err := newCipherSpec("AES128", "XTS", []byte(keyOf(16))); err == nil {
		t.Error("bogus mode should fail")
	}
}

// TestEncryptIVIsByteSafe 守住关键修复：iv 必须是可见 ASCII 字符串，不能是任意
// 随机字节——否则 Bark 服务器 JSON 编码时会用 U+FFFD 替换非法 UTF-8 字节，破坏 IV。
func TestEncryptIVIsByteSafe(t *testing.T) {
	for _, mode := range []string{"CBC", "GCM"} {
		spec, err := newCipherSpec("AES256", mode, []byte(keyOf(32)))
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		_, iv, err := spec.encrypt([]byte(`{"body":"x"}`))
		if err != nil {
			t.Fatalf("%s: encrypt: %v", mode, err)
		}
		if iv == "" {
			t.Fatalf("%s: iv should not be empty", mode)
		}
		for i, c := range []byte(iv) {
			if c < 0x20 || c > 0x7e {
				t.Errorf("%s: iv byte %d = %#x is not printable ASCII", mode, i, c)
			}
		}
		if mode == "CBC" && len(iv) != 16 {
			t.Errorf("CBC iv length = %d, want 16", len(iv))
		}
	}
}

// TestEncryptCBCOpenSSLInterop 证明我们的 AES-256-CBC 密文能被 openssl 以
// example.sh 同款方案解开：key / iv 都按「字符串的字节」处理。这正是 Bark 的
// 加密契约，跑通即说明 wire 层与 Bark App 兼容。
func TestEncryptCBCOpenSSLInterop(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not available")
	}
	keyStr := keyOf(32) // 32 字符 → AES-256 的 32 字节密钥
	spec, err := newCipherSpec("AES256", "CBC", []byte(keyStr))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"body":"test","sound":"birdsong"}`)
	ctB64, iv, err := spec.encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		t.Fatal(err)
	}
	// openssl 的 -K/-iv 需 hex；Bark 方案里 key/iv 都是「字符串的字节」。
	cmd := exec.Command(openssl, "enc", "-d", "-aes-256-cbc",
		"-K", hex.EncodeToString([]byte(keyStr)),
		"-iv", hex.EncodeToString([]byte(iv)))
	cmd.Stdin = bytes.NewReader(ct)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("openssl decrypt failed: %v\n%s", err, out)
	}
	if !bytes.Equal(out, plaintext) {
		t.Errorf("openssl decrypt mismatch:\n got  %q\n want %q", out, plaintext)
	}
}

func TestEncryptCBCUsesRandomIV(t *testing.T) {
	spec, err := newCipherSpec("AES128", "CBC", []byte(keyOf(16)))
	if err != nil {
		t.Fatal(err)
	}
	ct1, iv1, _ := spec.encrypt([]byte("same plaintext"))
	ct2, iv2, _ := spec.encrypt([]byte("same plaintext"))
	if iv1 == iv2 {
		t.Error("CBC IV should be random per encryption")
	}
	if ct1 == ct2 {
		t.Error("CBC ciphertext should differ with random IV")
	}
}
