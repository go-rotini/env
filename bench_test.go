package env_test

import (
	"io"
	"testing"
	"time"

	"github.com/go-rotini/env"
)

type benchConfig struct {
	Port    int           `env:"PORT,default=8080"`
	Host    string        `env:"HOST,default=localhost"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"`
	Hosts   []string      `env:"HOSTS,default=a;b;c,separator=;"`
	Debug   bool          `env:"DEBUG"`
	DB      struct {
		URL  string `env:"URL,default=postgres://x"`
		Pool int    `env:"POOL,default=4"`
	} `envPrefix:"DB_"`
}

func BenchmarkLoad(b *testing.B) {
	l := env.New(env.WithSource(env.Map(map[string]string{"DEBUG": "true", "PORT": "9000"})))
	b.ReportAllocs()
	for b.Loop() {
		var c benchConfig
		if err := l.Load(&c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadWithPrefix(b *testing.B) {
	l := env.New(env.WithPrefix("APP_"), env.WithSource(env.Map(map[string]string{"APP_DEBUG": "true"})))
	b.ReportAllocs()
	for b.Loop() {
		var c benchConfig
		if err := l.Load(&c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadWithTagName(b *testing.B) {
	l := env.New(env.WithTagName("cfg"), env.WithSource(env.Map(nil)))
	b.ReportAllocs()
	for b.Loop() {
		var c benchConfig
		if err := l.Load(&c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	var c benchConfig
	if err := env.New(env.WithSource(env.Map(nil))).Load(&c); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := env.Encode(io.Discard, &c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOSEnvLookup(b *testing.B) {
	s := env.OSEnv()
	b.Setenv("ENV_BENCH_X", "1")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = s.Lookup("ENV_BENCH_X")
	}
}

func BenchmarkLoadExpand(b *testing.B) {
	type cfg struct {
		URL string `env:"URL,expand"`
	}
	l := env.New(env.WithSource(env.Map(map[string]string{
		"URL":  "postgres://${USER}:${PASS:-x}@${HOST}:${PORT}/db?sslmode=${MODE:-disable}",
		"USER": "u", "HOST": "h", "PORT": "5432",
	})))
	b.ReportAllocs()
	for b.Loop() {
		var c cfg
		if err := l.Load(&c); err != nil {
			b.Fatal(err)
		}
	}
}
