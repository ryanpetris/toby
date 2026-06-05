package run

// Serving the Toby binary to the sandbox: binaryRoute streams the Linux Toby
// bytes over the control endpoint so the sandbox manager can download and exec
// them during startup.

import (
	"net/http"
	"strconv"
)

// binaryRoute serves the Toby binary bytes for the sandbox to download. Auth is
// enforced by the control server's token middleware (registered with Auth: true).
func binaryRoute(source func() ([]byte, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := source()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	})
}
