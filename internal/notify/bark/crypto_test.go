package bark

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
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
