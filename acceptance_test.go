package env_test

import (
	"testing"

	"github.com/go-rotini/env"
)

// Loader-level acceptance tests. Each test feeds the loader a [env.Map]
// source shaped like a real-world configuration set (the same key/value
// data a `.env` file from the named tool would produce) and runs the
// full pipeline (field collection → source lookup → decoding) against a
// struct shaped like that data. This exercises the LOADER end-to-end
// without depending on any particular source implementation; `.env`
// parsing itself is covered by github.com/go-rotini/dotenv's own
// acceptance suite.

// --- kubectl-style config: load into a typed config struct. ---

func TestAcceptance_KubectlLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type kubectlCfg struct {
		Host         string   `env:"DATABASE_HOST,required"`
		Port         int      `env:"DATABASE_PORT,required"`
		Name         string   `env:"DATABASE_NAME"`
		LogLevel     string   `env:"APP_LOG_LEVEL"`
		FeatureFlags []string `env:"FEATURE_FLAGS"`
	}

	src := env.Map(map[string]string{
		"DATABASE_HOST": "postgres.svc.cluster.local",
		"DATABASE_PORT": "5432",
		"DATABASE_NAME": "app",
		"APP_LOG_LEVEL": "info",
		"FEATURE_FLAGS": "feature-a,feature-b",
	})
	loader := env.New(env.WithSource(src))

	var cfg kubectlCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "postgres.svc.cluster.local" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.Port != 5432 {
		t.Errorf("Port = %d", cfg.Port)
	}
	if cfg.Name != "app" {
		t.Errorf("Name = %q", cfg.Name)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if len(cfg.FeatureFlags) != 2 || cfg.FeatureFlags[0] != "feature-a" || cfg.FeatureFlags[1] != "feature-b" {
		t.Errorf("FeatureFlags = %v", cfg.FeatureFlags)
	}
}

// --- GitHub Actions: typical runner-injected variables. ---

func TestAcceptance_GHALoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type ghaCfg struct {
		IsActions  bool   `env:"GITHUB_ACTIONS"`
		Repository string `env:"GITHUB_REPOSITORY,required"`
		Ref        string `env:"GITHUB_REF"`
		SHA        string `env:"GITHUB_SHA,required"`
		RunnerOS   string `env:"RUNNER_OS"`
		RunnerTemp string `env:"RUNNER_TEMP"`
	}

	src := env.Map(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_REPOSITORY": "acme/widget",
		"GITHUB_REF":        "refs/heads/main",
		"GITHUB_SHA":        "0123456789abcdef0123456789abcdef01234567",
		"RUNNER_OS":         "Linux",
		"RUNNER_TEMP":       "/home/runner/work/_temp",
	})
	loader := env.New(env.WithSource(src))

	var cfg ghaCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.IsActions {
		t.Error("IsActions = false")
	}
	if cfg.Repository != "acme/widget" {
		t.Errorf("Repository = %q", cfg.Repository)
	}
	if len(cfg.SHA) != 40 {
		t.Errorf("SHA len = %d, want 40", len(cfg.SHA))
	}
}

// --- Compose-style config: project + image tag. ---

func TestAcceptance_ComposeLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type composeCfg struct {
		Project string `env:"COMPOSE_PROJECT_NAME,required"`
		Tag     string `env:"TAG,required"`
		WebPort int    `env:"WEB_PORT"`
	}

	src := env.Map(map[string]string{
		"COMPOSE_PROJECT_NAME": "acme",
		"TAG":                  "v1.4.0",
		"DB_PASSWORD":          "changeme",
		"WEB_PORT":             "8080",
	})
	loader := env.New(env.WithSource(src))

	var cfg composeCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project != "acme" || cfg.Tag != "v1.4.0" || cfg.WebPort != 8080 {
		t.Errorf("cfg = %+v", cfg)
	}
}

// --- Rails-style config. ---

func TestAcceptance_RailsLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type railsCfg struct {
		Env           string `env:"RAILS_ENV,required"`
		DatabaseURL   string `env:"DATABASE_URL,required"`
		RedisURL      string `env:"REDIS_URL"`
		SecretKeyBase string `env:"SECRET_KEY_BASE,required,secret"`
	}

	src := env.Map(map[string]string{
		"RAILS_ENV":       "development",
		"DATABASE_URL":    "postgres://localhost/myapp_development",
		"REDIS_URL":       "redis://localhost:6379/0",
		"SECRET_KEY_BASE": "09f86d1f0ad2c5b71f6f58cae7b3b9c8",
	})
	loader := env.New(env.WithSource(src))

	var cfg railsCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != "development" {
		t.Errorf("Env = %q", cfg.Env)
	}
	if cfg.DatabaseURL != "postgres://localhost/myapp_development" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.SecretKeyBase != "09f86d1f0ad2c5b71f6f58cae7b3b9c8" {
		t.Errorf("SecretKeyBase = %q", cfg.SecretKeyBase)
	}
}

// --- Laravel-style config: APP_KEY with base64 prefix, set-but-empty DB_PASSWORD. ---

func TestAcceptance_LaravelLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type laravelCfg struct {
		AppName    string `env:"APP_NAME,required"`
		AppEnv     string `env:"APP_ENV"`
		AppKey     string `env:"APP_KEY,required,secret"`
		AppDebug   bool   `env:"APP_DEBUG"`
		AppURL     string `env:"APP_URL"`
		LogLevel   string `env:"LOG_LEVEL"`
		DBHost     string `env:"DB_HOST"`
		DBPort     int    `env:"DB_PORT"`
		DBName     string `env:"DB_DATABASE"`
		DBUser     string `env:"DB_USERNAME"`
		DBPassword string `env:"DB_PASSWORD"`
	}

	src := env.Map(map[string]string{
		"APP_NAME":      "Laravel",
		"APP_ENV":       "local",
		"APP_KEY":       "base64:b1jK2f5h+y0E9aDnM2gVkF8gJ3c3mY4cTx3R3F6nQGw=",
		"APP_DEBUG":     "true",
		"APP_URL":       "http://localhost",
		"LOG_CHANNEL":   "stack",
		"LOG_LEVEL":     "debug",
		"DB_CONNECTION": "mysql",
		"DB_HOST":       "127.0.0.1",
		"DB_PORT":       "3306",
		"DB_DATABASE":   "laravel",
		"DB_USERNAME":   "root",
		"DB_PASSWORD":   "", // set-but-empty
	})
	loader := env.New(env.WithSource(src))

	var cfg laravelCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AppName != "Laravel" || cfg.AppEnv != "local" || !cfg.AppDebug {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.DBPort != 3306 {
		t.Errorf("DBPort = %d", cfg.DBPort)
	}
	// Set-but-empty: DB_PASSWORD is present with an empty value. The
	// loader receives ok=true with value "" from the source and decodes
	// into the empty string (no notEmpty constraint).
	if cfg.DBPassword != "" {
		t.Errorf("DBPassword = %q, want empty", cfg.DBPassword)
	}
}

// --- Loader composes a file-shaped source under OSEnv: OSEnv wins. ---

func TestAcceptance_OSEnvBeatsLowerPrioritySource(t *testing.T) {
	t.Setenv("APP_DEBUG", "false")

	fileLike := env.Map(map[string]string{"APP_DEBUG": "true"})
	loader := env.New(env.WithSource(env.OSEnv(), fileLike))

	type cfg struct {
		AppDebug bool `env:"APP_DEBUG"`
	}
	var got cfg
	if err := loader.Load(&got); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AppDebug {
		t.Errorf("AppDebug = true, want false (OSEnv override should beat the lower-priority source)")
	}
}
