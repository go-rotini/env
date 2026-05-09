package env_test

import (
	"path/filepath"
	"testing"

	"github.com/go-rotini/env"
	"github.com/go-rotini/env/dotenv"
)

// Loader-level acceptance tests. Each test composes a `dotenv` source
// for a real-world `.env` fixture and runs the loader through the
// full pipeline (field collection → source lookup → decoding) against
// a struct shaped like the fixture's data. This catches regressions in
// the LOADER's interaction with dotenv that the dotenv-internal tests
// in [dotenv/acceptance_test.go] would not.
//
// Spec: §8.3.

func acceptanceEnvPath(name string) string {
	return filepath.Join("testdata", "acceptance", name+".env")
}

// --- kubectl: load into a typed config struct. ---

func TestAcceptance_KubectlLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type kubectlCfg struct {
		Host         string   `env:"DATABASE_HOST,required"`
		Port         int      `env:"DATABASE_PORT,required"`
		Name         string   `env:"DATABASE_NAME"`
		LogLevel     string   `env:"APP_LOG_LEVEL"`
		FeatureFlags []string `env:"FEATURE_FLAGS"`
	}

	src, err := dotenv.NewSource(acceptanceEnvPath("kubectl"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
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

	src, err := dotenv.NewSource(acceptanceEnvPath("gha"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
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

// --- Compose .env: project + image tag. ---

func TestAcceptance_ComposeLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type composeCfg struct {
		Project string `env:"COMPOSE_PROJECT_NAME,required"`
		Tag     string `env:"TAG,required"`
		WebPort int    `env:"WEB_PORT"`
	}

	src, err := dotenv.NewSource(acceptanceEnvPath("compose"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	loader := env.New(env.WithSource(src))

	var cfg composeCfg
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project != "acme" || cfg.Tag != "v1.4.0" || cfg.WebPort != 8080 {
		t.Errorf("cfg = %+v", cfg)
	}
}

// --- Rails: `export ` prefix transparently accepted. ---

func TestAcceptance_RailsLoadsIntoStruct(t *testing.T) {
	t.Parallel()
	type railsCfg struct {
		Env           string `env:"RAILS_ENV,required"`
		DatabaseURL   string `env:"DATABASE_URL,required"`
		RedisURL      string `env:"REDIS_URL"`
		SecretKeyBase string `env:"SECRET_KEY_BASE,required,secret"`
	}

	src, err := dotenv.NewSource(acceptanceEnvPath("rails"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
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

// --- Laravel: APP_KEY with base64 prefix, set-but-empty DB_PASSWORD. ---

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

	src, err := dotenv.NewSource(acceptanceEnvPath("laravel"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
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
	// Set-but-empty: DB_PASSWORD= is present in the file. The loader
	// receives ok=true with value "" from the source and decodes
	// into the empty string (no notEmpty constraint).
	if cfg.DBPassword != "" {
		t.Errorf("DBPassword = %q, want empty", cfg.DBPassword)
	}
}

// --- Loader composes dotenv source with OSEnv: OSEnv wins. ---

func TestAcceptance_OSEnvBeatsDotEnvFile(t *testing.T) {
	t.Setenv("APP_DEBUG", "false")

	src, err := dotenv.NewSource(acceptanceEnvPath("laravel"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	loader := env.New(env.WithSource(env.OSEnv(), src))

	type cfg struct {
		AppDebug bool `env:"APP_DEBUG"`
	}
	var got cfg
	if err := loader.Load(&got); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AppDebug {
		t.Errorf("AppDebug = true, want false (OSEnv override should beat dotenv source)")
	}
}
