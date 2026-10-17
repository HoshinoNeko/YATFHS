package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const MaxExpireSec = 10 * 365 * 24 * 3600

type RateLimit struct {
	UploadRequestsPerMin   int   `json:"upload_requests_per_min"`
	DownloadRequestsPerMin int   `json:"download_requests_per_min"`
	UploadBytesPerHour     int64 `json:"upload_bytes_per_hour"`
}

type ExpiryPolicy struct {
	DefaultExpireSec int   `json:"default_expire_sec"`
	MinExpireSec     int   `json:"min_expire_sec"`
	MaxExpireSec     int   `json:"max_expire_sec"`
	ExpireOptionsSec []int `json:"expire_options_sec"`
}

func (p ExpiryPolicy) Validate() error {
	if p.MinExpireSec < 1 || p.MaxExpireSec > MaxExpireSec || p.MinExpireSec > p.MaxExpireSec || p.DefaultExpireSec < p.MinExpireSec || p.DefaultExpireSec > p.MaxExpireSec {
		return fmt.Errorf("expiry must satisfy 1 <= min <= default <= max <= %d", MaxExpireSec)
	}
	if len(p.ExpireOptionsSec) == 0 || len(p.ExpireOptionsSec) > 20 {
		return fmt.Errorf("provide between 1 and 20 expiry options")
	}
	seen := map[int]bool{}
	for _, n := range p.ExpireOptionsSec {
		if n < p.MinExpireSec || n > p.MaxExpireSec || seen[n] {
			return fmt.Errorf("expiry options must be unique and within min/max")
		}
		seen[n] = true
	}
	return nil
}

type Config struct {
	Host               string        `json:"host"`
	Port               string        `json:"port"`
	BaseURL            string        `json:"base_url"`
	StorageDir         string        `json:"storage_dir"`
	MaxFileSizeBytes   int64         `json:"max_file_size_bytes"`
	DefaultExpireSec   int           `json:"default_expire_sec"`
	MinExpireSec       int           `json:"min_expire_sec"`
	MaxExpireSec       int           `json:"max_expire_sec"`
	ExpireOptionsSec   []int         `json:"expire_options_sec"`
	AdminToken         string        `json:"admin_token,omitempty"`
	CleanupInterval    time.Duration `json:"-"`
	CleanupIntervalStr string        `json:"cleanup_interval"`
	RateLimit          RateLimit     `json:"rate_limit"`
	IPWhitelist        []string      `json:"ip_whitelist"`
	TrustedProxies     []string      `json:"trusted_proxies"`
}

func Default() *Config {
	return &Config{Host: "0.0.0.0", Port: "8080", StorageDir: "./data", MaxFileSizeBytes: 100 * 1024 * 1024,
		DefaultExpireSec: 3600, MinExpireSec: 60, MaxExpireSec: 172800, ExpireOptionsSec: []int{3600, 14400, 43200, 86400, 172800},
		CleanupInterval: 5 * time.Minute, CleanupIntervalStr: "5m", RateLimit: RateLimit{10, 60, 500 * 1024 * 1024}, IPWhitelist: []string{}, TrustedProxies: []string{}}
}
func (c *Config) Policy() ExpiryPolicy {
	return ExpiryPolicy{c.DefaultExpireSec, c.MinExpireSec, c.MaxExpireSec, append([]int(nil), c.ExpireOptionsSec...)}
}
func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	}
	if token, ok := os.LookupEnv("YATFHS_ADMIN_TOKEN"); ok {
		cfg.AdminToken = token
	}
	if cfg.AdminToken != "" && len(cfg.AdminToken) < 32 {
		return nil, fmt.Errorf("admin token must contain at least 32 characters")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.MaxFileSizeBytes < 1 || cfg.MaxFileSizeBytes > 1<<40 {
		return nil, fmt.Errorf("max_file_size_bytes must be between 1 and 1 TiB")
	}
	cfg.CleanupInterval, err = time.ParseDuration(cfg.CleanupIntervalStr)
	if err != nil || cfg.CleanupInterval <= 0 {
		return nil, fmt.Errorf("cleanup_interval must be a positive duration")
	}
	// Preserve compatibility with older configs containing a narrower expiry range.
	if !strings.Contains(string(b), `"expire_options_sec"`) {
		cfg.ExpireOptionsSec = nil
		for _, n := range []int{3600, 14400, 43200, 86400, 172800} {
			if n >= cfg.MinExpireSec && n <= cfg.MaxExpireSec {
				cfg.ExpireOptionsSec = append(cfg.ExpireOptionsSec, n)
			}
		}
		if len(cfg.ExpireOptionsSec) == 0 {
			cfg.ExpireOptionsSec = []int{cfg.DefaultExpireSec}
		}
	}
	if err := cfg.Policy().Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
