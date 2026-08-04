package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvDoesNotOverwriteEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GHR_DOTENV_NEW=loaded\nGHR_DOTENV_EXISTING=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHR_DOTENV_EXISTING", "process")
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GHR_DOTENV_NEW") != "loaded" || os.Getenv("GHR_DOTENV_EXISTING") != "process" {
		t.Fatalf("unexpected dotenv values: new=%q existing=%q", os.Getenv("GHR_DOTENV_NEW"), os.Getenv("GHR_DOTENV_EXISTING"))
	}
}
