package config

import (
	"log"
	"os"
	"strconv"
)

type Config struct {
	AppPort              string
	OracleDSN            string
	KubeconfigPath       string
	MaxParallelTests     int
	K6Image              string
	K6TimeoutSeconds     int
	SessionSecret        string
	SessionEncryptKey    string
	GrafanaSkipTLSVerify bool
	//  For later
	//  See internal/backend/grafana/client.go
	//  GrafanaURL         string
	//  GrafanaToken       string
	// TODO move os.Getenv("GRAFANA_URL") / os.Getenv("GRAFANA_TOKEN") here — https://trello.com/c/IcypCnrh
}

func Load() *Config {
	maxParallel := 4
	if v := os.Getenv("MAX_PARALLEL_TESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxParallel = n
		}
	}

	k6Timeout := 300
	if v := os.Getenv("K6_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			k6Timeout = n
		}
	}

	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		sessionSecret = "change-me-in-production-please-use-32-bytes"
	}

	sessionEncryptKey := os.Getenv("SESSION_ENCRYPT_KEY")
	if sessionEncryptKey == "" {
		// Fallback keeps the app startable without config changes, but the
		// cookie contents will be readable in DevTools. Set a real value in
		// production — 16, 24, or 32 random bytes, distinct from SESSION_SECRET.
		sessionEncryptKey = "change-me-encrypt-key-16-bytes!!"
		log.Println("WARN: SESSION_ENCRYPT_KEY not set — using insecure default. Set a real value in production.")
	}

	return &Config{
		AppPort:              getEnv("APP_PORT", "8080"),
		OracleDSN:            os.Getenv("ORACLE_DSN"),
		KubeconfigPath:       os.Getenv("KUBECONFIG_PATH"),
		MaxParallelTests:     maxParallel,
		K6Image:              getEnv("K6_IMAGE", "grafana/k6:latest"),
		K6TimeoutSeconds:     k6Timeout,
		SessionSecret:        sessionSecret,
		SessionEncryptKey:    sessionEncryptKey,
		GrafanaSkipTLSVerify: os.Getenv("GRAFANA_SKIP_TLS_VERIFY") == "true",
		//  For later
		//  See internal/backend/grafana/client.go
		//  GrafanaURL         string
		//  GrafanaToken       string
		// https://trello.com/c/IcypCnrh
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
