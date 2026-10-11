// Command linkprobe loads a config through env's main entry points. The
// package tests build it to check what such a program links.
package main

import (
	"fmt"
	"time"

	"github.com/go-rotini/env"
)

type config struct {
	Port    int                `env:"PORT,default=8080"`
	Timeout time.Duration      `env:"TIMEOUT,default=5s"`
	Token   env.Secret[string] `env:"TOKEN"`
}

func main() {
	var c config
	if err := env.Load(&c); err != nil {
		fmt.Println(env.FormatError(err))
	}
	c2, err := env.LoadTo[config](env.WithPrefix("APP_"))
	fmt.Println(c.Port, c2.Timeout, c.Token, err)
}
