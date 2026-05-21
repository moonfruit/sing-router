package bark

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// cipherSpec 持有校验过的 AES 密钥与模式，用于 Bark 端到端加密推送。
//
// Bark 官方文档明确演示的是 AES-CBC；GCM / ECB 按标准 AES 模式实现，需用户在
// Bark App 侧配置一致的算法。加密为可选特性，默认关闭。
type cipherSpec struct {
	mode string // "CBC" / "ECB" / "GCM"
	key  []byte
}

// newCipherSpec 校验 algorithm 与 key 长度、mode 合法性后构造 cipherSpec。
func newCipherSpec(algorithm, mode string, key []byte) (*cipherSpec, error) {
	want := 0
	switch strings.ToUpper(strings.ReplaceAll(algorithm, "-", "")) {
	case "AES128", "":
		want = 16
	case "AES192":
		want = 24
	case "AES256":
		want = 32
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm %q (want AES128/AES192/AES256)", algorithm)
	}
	if len(key) != want {
		return nil, fmt.Errorf("encryption key for %s must be %d bytes, got %d", algorithm, want, len(key))
	}
	m := strings.ToUpper(mode)
	if m == "" {
		m = "CBC"
	}
	switch m {
	case "CBC", "ECB", "GCM":
	default:
		return nil, fmt.Errorf("unsupported encryption mode %q (want CBC/ECB/GCM)", mode)
	}
	return &cipherSpec{mode: m, key: key}, nil
}

// ivCharset 是随机 IV 的取值字符集。Bark 把 iv 参数当**字符串**、直接用其字节
// 作为 AES IV（见 example.sh：iv='0000000000000000' 是 16 字符串）。所以 IV 必须
// 是字节安全的可见 ASCII 定长字符串——不能是任意随机字节：非 UTF-8 字节会在
// Bark 服务器 JSON 编码 APNs 负载时被 Go encoding/json 替换成 U+FFFD，破坏 IV
// 导致 App 侧 “Decryption failed”。
const ivCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomIVString 生成 n 个字符的随机 IV 字符串；n 即 AES IV/nonce 的字节数。
func randomIVString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = ivCharset[int(buf[i])%len(ivCharset)]
	}
	return string(buf), nil
}

// encrypt 加密 plaintext，返回 (base64 密文, iv 字符串)。ECB 无 iv，返回空串。
// iv 是随机生成的可见 ASCII 字符串，由 caller 作为 Bark 的 iv 参数随密文一起发送；
// 其字节同时就是 AES 的 IV/nonce。
func (s *cipherSpec) encrypt(plaintext []byte) (ciphertext, iv string, err error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", "", err
	}
	switch s.mode {
	case "CBC":
		iv, err := randomIVString(aes.BlockSize)
		if err != nil {
			return "", "", err
		}
		padded := pkcs7Pad(plaintext, aes.BlockSize)
		out := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(out, padded)
		return base64.StdEncoding.EncodeToString(out), iv, nil
	case "ECB":
		padded := pkcs7Pad(plaintext, aes.BlockSize)
		out := make([]byte, len(padded))
		for i := 0; i < len(padded); i += aes.BlockSize {
			block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
		}
		return base64.StdEncoding.EncodeToString(out), "", nil
	case "GCM":
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return "", "", err
		}
		nonce, err := randomIVString(gcm.NonceSize())
		if err != nil {
			return "", "", err
		}
		out := gcm.Seal(nil, []byte(nonce), plaintext, nil)
		return base64.StdEncoding.EncodeToString(out), nonce, nil
	default:
		return "", "", fmt.Errorf("unsupported encryption mode %q", s.mode)
	}
}

// pkcs7Pad 把 data 补齐到 blockSize 的整数倍（CBC/ECB 需要）。
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}
