package config

import (
	"encoding/json"
	"os"
	"time"
)

type RateLimit struct {
	UploadRequestsPerMin   int `json:"upload_requests_per_min"`
	DownloadRequestsPerMin int `json:"download_requests_per_min"`
	UploadBytesPerHour     int64 `json:"upload_bytes_per_hour"`
}

type Config struct {
	Host            string            `json:"host"`
	Port            string            `json:"port"`
	BaseURL         string            `json:"base_url"`
	StorageDir      string            `json:"storage_dir"`
	MaxFileSizeBytes int64            `json:"max_file_size_bytes"`
	DefaultExpireSec int              `json:"default_expire_sec"`
	MinExpireSec     int              `json:"min_expire_sec"`
	MaxExpireSec     int              `json:"max_expire_sec"`
	CleanupInterval  time.Duration    `json:"-"`
	CleanupIntervalStr string         `json:"cleanup_interval"`
	RateLimit        RateLimit        `json:"rate_limit"`
	IPWhitelist      []string         `json:"ip_whitelist"`
}

func Default() *Config {
	return &Config{
		Host:             "0.0.0.0",
		Port:             "8080",
		BaseURL:          "http://localhost:8080",
		StorageDir:       "./data",
		MaxFileSizeBytes: 100 * 1024 * 1024, // 100 MB
		DefaultExpireSec: 3600,
		MinExpireSec:     60,
		MaxExpireSec:     172800,
		CleanupInterval:  5 * time.Minute,
		CleanupIntervalStr: "5m",
		RateLimit: RateLimit{
			UploadRequestsPerMin:   10,
			DownloadRequestsPerMin: 60,
			UploadBytesPerHour:     500 * 1024 * 1024, // 500 MB/hr
		},
		IPWhitelist: []string{},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(cfg); err != nil {
		return nil, err
	}

	if cfg.CleanupIntervalStr != "" {
		d, err := time.ParseDuration(cfg.CleanupIntervalStr)
		if err == nil {
			cfg.CleanupInterval = d
		}
	}

	return cfg, nil
}
