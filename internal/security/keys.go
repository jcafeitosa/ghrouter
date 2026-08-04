package security

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

type ClientKeys struct {
	GitHub    string `json:"github"`
	OpenAI    string `json:"openai"`
	Anthropic string `json:"anthropic"`
}

func LoadOrCreate(path string) (ClientKeys, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	if info, err := os.Lstat(path); err == nil {
		if err := secureExistingKeyPath(path, info); err != nil {
			return ClientKeys{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return ClientKeys{}, fmt.Errorf("open client keys: %w", err)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return ClientKeys{}, fmt.Errorf("read client keys: %w", readErr)
		}
		if closeErr != nil {
			return ClientKeys{}, fmt.Errorf("close client keys: %w", closeErr)
		}
		var keys ClientKeys
		if err := json.Unmarshal(data, &keys); err != nil {
			return ClientKeys{}, fmt.Errorf("decode client keys: %w", err)
		}
		if err := validate(keys); err != nil {
			return ClientKeys{}, err
		}
		return keys, nil
	} else if !os.IsNotExist(err) {
		return ClientKeys{}, fmt.Errorf("read client keys: %w", err)
	}
	keys, err := newClientKeys()
	if err != nil {
		return ClientKeys{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ClientKeys{}, fmt.Errorf("create key directory: %w", err)
	}
	if err := secureKeyDirectory(filepath.Dir(path)); err != nil {
		return ClientKeys{}, err
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return ClientKeys{}, fmt.Errorf("encode client keys: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return LoadOrCreate(path)
		}
		return ClientKeys{}, fmt.Errorf("create client keys: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return ClientKeys{}, fmt.Errorf("write client keys: %w", err)
	}
	if err := file.Close(); err != nil {
		return ClientKeys{}, fmt.Errorf("close client keys: %w", err)
	}
	return keys, nil
}

func secureExistingKeyPath(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("client key path is not a regular file")
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("client key file is not owned by the current user")
	}
	parent := filepath.Dir(path)
	if err := secureKeyDirectory(parent); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("harden client key file: %w", err)
	}
	return nil
}

func secureKeyDirectory(parent string) error {
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect client key directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("client key directory is not a real directory")
	}
	if !ownedByCurrentUser(parentInfo) {
		return fmt.Errorf("client key directory is not owned by the current user")
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("harden client key directory: %w", err)
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	current, err := user.Current()
	if err != nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	uid := value.FieldByName("Uid")
	if !uid.IsValid() {
		return false
	}
	var owner string
	switch uid.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		owner = strconv.FormatUint(uid.Uint(), 10)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		owner = strconv.FormatInt(uid.Int(), 10)
	default:
		return false
	}
	return owner == current.Uid
}

func DefaultPath() string {
	if root, err := os.UserCacheDir(); err == nil && root != "" {
		return filepath.Join(root, "ghrouter", "client-keys.json")
	}
	return filepath.Join(os.TempDir(), "ghrouter-client-keys.json")
}

func (k ClientKeys) For(client string) string {
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "copilot", "github", "gh", "gh-copilot":
		return k.GitHub
	case "claude", "anthropic", "claude-code":
		return k.Anthropic
	case "cursor":
		return k.OpenAI
	default:
		return ""
	}
}

func (k ClientKeys) Masked() map[string]string {
	return map[string]string{
		"github":    MaskKey(k.GitHub),
		"openai":    MaskKey(k.OpenAI),
		"anthropic": MaskKey(k.Anthropic),
	}
}

func MaskKey(key string) string {
	if len(key) <= 8 {
		return "********"
	}
	return key[:min(len(key), 18)] + "…" + key[len(key)-4:]
}

func newClientKeys() (ClientKeys, error) {
	github, err := randomKey("ghr_gh_")
	if err != nil {
		return ClientKeys{}, err
	}
	openAI, err := randomKey("sk-ghrouter-")
	if err != nil {
		return ClientKeys{}, err
	}
	anthropic, err := randomKey("sk-ant-ghrouter-")
	if err != nil {
		return ClientKeys{}, err
	}
	return ClientKeys{GitHub: github, OpenAI: openAI, Anthropic: anthropic}, nil
}

func randomKey(prefix string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate client key: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func validate(keys ClientKeys) error {
	if keys.GitHub == "" || keys.OpenAI == "" || keys.Anthropic == "" {
		return fmt.Errorf("client key file is incomplete")
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
