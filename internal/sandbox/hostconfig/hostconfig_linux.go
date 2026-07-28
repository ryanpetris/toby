//go:build linux

// Package hostconfig copies selected host configuration into sandbox root
// overlays.
package hostconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"petris.dev/toby/internal/sandbox/bwrap"
)

const copiedFileMode = 0o644

// PrivateResolverAddress is the synthetic DNS address exposed by Pasta inside
// private sidecar network namespaces.
const PrivateResolverAddress = "198.51.100.53"

type fileSpec struct {
	source   string
	target   string
	data     []byte
	optional bool
}

type copiedFile struct {
	target string
	data   []byte
}

// CopyHost reads the current host files through ordinary filesystem semantics
// and writes their contents as regular files in the supplied root overlay.
func CopyHost(directories *bwrap.RunDirectories) error {
	specs := []fileSpec{
		{
			source: "/etc/resolv.conf",
			target: "etc/resolv.conf",
		},
	}
	return copyFiles(directories, append(specs, commonFiles()...))
}

// CopyPrivate writes the static Pasta resolver and copies the remaining host
// configuration as regular files in the supplied root overlay.
func CopyPrivate(directories *bwrap.RunDirectories) error {
	specs := []fileSpec{
		{
			target: "etc/resolv.conf",
			data: []byte(
				"nameserver " + PrivateResolverAddress + "\n",
			),
		},
	}
	return copyFiles(directories, append(specs, commonFiles()...))
}

func commonFiles() []fileSpec {
	return []fileSpec{
		{
			source:   "/etc/hosts",
			target:   "etc/hosts",
			optional: true,
		},
		{
			source:   "/etc/localtime",
			target:   "etc/localtime",
			optional: true,
		},
	}
}

func copyFiles(
	directories *bwrap.RunDirectories,
	specs []fileSpec,
) error {
	if directories == nil {
		return fmt.Errorf("copy host configuration: run directories are nil")
	}

	copied := make([]copiedFile, 0, len(specs))
	for _, spec := range specs {
		data := append([]byte(nil), spec.data...)
		if spec.source != "" {
			var err error
			data, err = os.ReadFile(spec.source)
			if spec.optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf(
					"read host configuration %q: %w",
					spec.source,
					err,
				)
			}
		}
		copied = append(copied, copiedFile{
			target: spec.target,
			data:   data,
		})
	}

	for _, file := range copied {
		if err := directories.ReplaceOverlayFile(
			file.target,
			file.data,
			copiedFileMode,
		); err != nil {
			return fmt.Errorf(
				"write sandbox host configuration %q: %w",
				file.target,
				err,
			)
		}
	}

	return nil
}
