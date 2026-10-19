// Package config mirrors daemon/config.py of the upstream Hub, plus the knobs
// that let a test instance live next to a production one.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	// Daemon's own bind. Caddy sits in front on PublicPort.
	Host string
	Port int

	// The one port the outside world sees.
	PublicPort   int
	ProxyEnabled bool

	BaseDir      string
	RegistryPath string
	// RegistryHome is the single on-disk source of truth for recipes:
	// sources.yaml, vendored/ clones and local/ recipes live below it.
	RegistryHome string
	DataDir      string
	DBPath       string

	AuthEnabled       bool
	SessionCookieName string
	SessionTTLDays    int
	ForceSecureCookie bool

	// Project names *our* own containers (the Caddy front door) so a second Hub
	// does not fight the first one over the proxy container.
	Project string

	// Recipes hardcode `spark-ai-hub-net` and `spark-ai-hub-<slug>` inside their
	// compose files and container_name fields, so the app-facing names are data,
	// not a knob: changing them would break every recipe on this device.
	AppNetwork       string
	AppProjectPrefix string
}

func Load() Config {
	base, _ := os.Getwd()
	c := Config{
		Host:              env("HOST", "0.0.0.0"),
		Port:              envInt("PORT", 9110),
		PublicPort:        envInt("PUBLIC_PORT", 9100),
		ProxyEnabled:      envBool("PROXY_ENABLED", true),
		BaseDir:           base,
		AuthEnabled:       envBool("AUTH_ENABLED", true),
		SessionCookieName: env("SESSION_COOKIE_NAME", "spark_ai_hub_session"),
		SessionTTLDays:    envInt("SESSION_TTL_DAYS", 30),
		ForceSecureCookie: envBool("FORCE_SECURE_COOKIE", false),
		Project:           env("PROJECT", "gospark-hub"),
		AppNetwork:        env("APP_NETWORK", "spark-ai-hub-net"),
		AppProjectPrefix:  env("APP_PROJECT_PREFIX", "spark-ai-hub"),
	}
	if v := os.Getenv("BASE_DIR"); v != "" {
		c.BaseDir = v
	}
	c.RegistryPath = envPath("REGISTRY_PATH", filepath.Join(c.BaseDir, "registry", "recipes"))
	c.RegistryHome = envPath("REGISTRY_HOME", defaultRegistryHome())
	c.DataDir = envPath("DATA_DIR", filepath.Join(c.BaseDir, "data"))
	c.DBPath = envPath("DB_PATH", filepath.Join(c.DataDir, "spark-ai-hub.db"))
	return c
}

// env reads SPARK_AI_HUB_<name> so an existing deployment's variables keep
// working; bare <name> is accepted as a shortcut.
func env(name, def string) string {
	if v, ok := os.LookupEnv("SPARK_AI_HUB_" + name); ok {
		return v
	}
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(env(name, ""))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(env(name, "")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func defaultRegistryHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "gospark", "registry")
	}
	return filepath.Join(home, ".config", "gospark", "registry")
}

func envPath(name, def string) string {
	v := env(name, "")
	if v == "" {
		return def
	}
	if strings.HasPrefix(v, "~") {
		home, _ := os.UserHomeDir()
		v = filepath.Join(home, v[1:])
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return def
	}
	return abs
}
