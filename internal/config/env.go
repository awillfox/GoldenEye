package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Env holds values loaded from .env / environment variables.
// These are infrastructure/secret values set at deploy time and never written back.
type Env struct {
	Port           int    `mapstructure:"PORT"`
	PSQLURL        string `mapstructure:"PSQL_URL"`
	MasterKey      string `mapstructure:"MASTER_KEY"`
	AdminUser      string `mapstructure:"ADMIN_USER"`
	AdminPass      string `mapstructure:"ADMIN_PASS"`
	RecordingsDir  string `mapstructure:"RECORDINGS_DIR"`
	MediamtxAPI    string `mapstructure:"MEDIAMTX_API"`
	MediamtxHLS    string `mapstructure:"MEDIAMTX_HLS"`
	MediamtxWebRTC string `mapstructure:"MEDIAMTX_WEBRTC"`
	MediamtxRTSP   string `mapstructure:"MEDIAMTX_RTSP"`
}

// LoadEnv loads config from .env file (if present) then environment variables.
// Environment variables always win over .env file values.
func LoadEnv() (*Env, error) {
	v := viper.New()

	// Try to read .env from working directory.
	v.SetConfigFile(".env")
	v.SetConfigType("dotenv")
	_ = v.ReadInConfig() // silent — missing .env is fine in production

	// Env vars override file values. Replace . with _ so nested keys work.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("PORT", 8080)
	v.SetDefault("RECORDINGS_DIR", "./recordings")
	v.SetDefault("MEDIAMTX_API", "http://127.0.0.1:9997")
	v.SetDefault("MEDIAMTX_HLS", "http://127.0.0.1:8888")
	v.SetDefault("MEDIAMTX_WEBRTC", "http://127.0.0.1:8889")
	v.SetDefault("MEDIAMTX_RTSP", "rtsp://127.0.0.1:8554")

	var env Env
	if err := v.Unmarshal(&env); err != nil {
		return nil, fmt.Errorf("unmarshal env config: %w", err)
	}

	if err := env.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(env.RecordingsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create recordings dir %q: %w", env.RecordingsDir, err)
	}

	return &env, nil
}

func (e *Env) validate() error {
	if e.PSQLURL == "" {
		return fmt.Errorf("PSQL_URL is required")
	}
	if e.MasterKey == "" {
		return fmt.Errorf("MASTER_KEY is required")
	}
	if len(e.MasterKey) < 32 {
		return fmt.Errorf("MASTER_KEY must be at least 32 bytes")
	}
	return nil
}
