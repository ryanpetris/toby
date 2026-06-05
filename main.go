// Command toby is the entry point for the Toby CLI; it delegates to the app
// package's composition root.
package main

import (
	"petris.dev/toby/internal/dirty/app"
)

func main() {
	app.Run()
}
