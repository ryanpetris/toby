package main

import (
	"petris.dev/toby/internal/app"

	"go.uber.org/fx"
)

func main() {
	fx.New(app.Module()).Run()
}
