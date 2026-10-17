package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidationAndOldConfigs(t *testing.T) {
	t.Setenv("YATFHS_ADMIN_TOKEN", strings.Repeat("a", 32))
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"min_expire_sec":120,"default_expire_sec":180,"max_expire_sec":600}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ExpireOptionsSec) != 1 || cfg.ExpireOptionsSec[0] != 180 {
		t.Fatal("old config not compatible")
	}
	if len(cfg.AdminToken) != 32 {
		t.Fatal("token env not applied")
	}
	for _, text := range []string{`{"min_expire_sec":0}`, `{"max_expire_sec":10}`, `{"max_file_size_bytes":0}`, `{"cleanup_interval":"0s"}`, `{"expire_options_sec":[3600,3600]}`, `{"expire_options_sec":[0]}`} {
		os.WriteFile(path, []byte(text), 0600)
		if _, err = Load(path); err == nil {
			t.Fatalf("accepted invalid config %s", text)
		}
	}
	t.Setenv("YATFHS_ADMIN_TOKEN", "short")
	if _, err = Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("short admin token accepted")
	}
}
