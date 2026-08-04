package providers

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"ghrouter/internal/types"
)

const NVIDIABaseURL = "https://integrate.api.nvidia.com"

var nvidiaAccountCursors sync.Map

func NormalizeNVIDIAProvider(provider *types.Provider) {
	if provider == nil || provider.Type != types.ProviderNVIDIA {
		return
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		provider.BaseURL = NVIDIABaseURL
	}
	if provider.AuthMethod == "" {
		provider.AuthMethod = types.AuthEnv
	}
	if provider.AuthConfig == nil {
		provider.AuthConfig = make(map[string]string)
	}
}

func nvidiaAPIKey(provider *types.Provider) string {
	if key := nvidiaAccountKey(provider); key != "" {
		return key
	}
	if provider != nil && provider.AuthConfig != nil {
		if value := strings.TrimSpace(provider.AuthConfig["api_key"]); value != "" {
			return value
		}
		if envName := strings.TrimSpace(provider.AuthConfig["api_key_env"]); envName != "" {
			return strings.TrimSpace(os.Getenv(envName))
		}
	}
	return strings.TrimSpace(os.Getenv("NVIDIA_API_KEY"))
}

func nvidiaAccountKey(provider *types.Provider) string {
	if provider == nil || len(provider.Accounts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(provider.Accounts))
	for _, account := range provider.Accounts {
		if !account.Enabled {
			continue
		}
		key := strings.TrimSpace(account.APIKey)
		if key == "" && strings.TrimSpace(account.APIKeyEnv) != "" {
			key = strings.TrimSpace(os.Getenv(account.APIKeyEnv))
		}
		if key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = provider.BaseURL
	}
	value, _ := nvidiaAccountCursors.LoadOrStore(name, &atomic.Uint64{})
	cursor := value.(*atomic.Uint64)
	index := cursor.Add(1) - 1
	return keys[index%uint64(len(keys))]
}
