package caddy

// Builds the secret-free initial native JSON read from Caddy's anonymous
// standard-input capability.

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const bootstrapMaximumBytes = 64 << 10

func bootstrapConfig() ([]byte, error) {
	config := map[string]any{
		"admin": map[string]any{
			"listen": unixAddress(defaultAdminSocket, "0600"),
			"config": map[string]any{
				"persist": false,
			},
		},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{
					"writer": map[string]any{
						"output": "discard",
					},
				},
			},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf(
			"encode Caddy bootstrap configuration",
		)
	}
	if len(data) == 0 || len(data) > bootstrapMaximumBytes {
		return nil, fmt.Errorf(
			"caddy bootstrap configuration is invalid",
		)
	}

	return data, nil
}

func bootstrapFile(logger *diagnostic.Logger) (*os.File, error) {
	data, err := bootstrapConfig()
	if err != nil {
		return nil, err
	}

	fd, err := unix.MemfdCreate(
		"toby-caddy-bootstrap",
		unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create Caddy bootstrap capability",
		)
	}
	file := os.NewFile(uintptr(fd), "Caddy bootstrap capability")
	if file == nil {
		err := fmt.Errorf("create Caddy bootstrap capability")
		logger.DebugError(
			"close invalid Caddy bootstrap descriptor",
			unix.Close(fd),
		)
		return nil, err
	}

	if _, err := file.Write(data); err != nil {
		writeErr := fmt.Errorf(
			"write Caddy bootstrap capability: %w",
			err,
		)
		logger.DebugError(
			"close Caddy bootstrap capability after write failure",
			file.Close(),
		)
		return nil, writeErr
	}
	if _, err := file.Seek(0, 0); err != nil {
		seekErr := fmt.Errorf(
			"rewind Caddy bootstrap capability: %w",
			err,
		)
		logger.DebugError(
			"close Caddy bootstrap capability after seek failure",
			file.Close(),
		)
		return nil, seekErr
	}

	const seals = unix.F_SEAL_SHRINK |
		unix.F_SEAL_GROW |
		unix.F_SEAL_WRITE |
		unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		sealErr := fmt.Errorf(
			"seal Caddy bootstrap capability: %w",
			err,
		)
		logger.DebugError(
			"close Caddy bootstrap capability after seal failure",
			file.Close(),
		)
		return nil, sealErr
	}

	return file, nil
}

func unixAddress(socket string, mode string) string {
	return "unix/" + socket + "|" + mode
}
