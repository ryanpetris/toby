//go:build linux

package cli

// Exercises the volume CLI against the native persistent store.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/storage"

	"gopkg.in/yaml.v3"
)

func TestVolumeCommandsListInspectAndRemove(t *testing.T) {
	paths := config.Paths{XDGDataHome: t.TempDir()}
	service, err := storage.NewStore(
		paths,
		storage.DefaultLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	home, err := service.ResolveHome(
		t.Context(),
		"demo",
		"work",
		storage.SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := home.Identity().ID
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	var listOutput bytes.Buffer
	list := NewRootCommand(Params{
		Registry: emptyRegistry(t),
		Paths:    paths,
		Args:     []string{"volume", "list"},
		Stdout:   &listOutput,
	})
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ID",
		"TYPE",
		"NAME",
		"PROFILE",
		id[:12],
		"home",
		"demo",
		"work",
	} {
		if !strings.Contains(listOutput.String(), want) {
			t.Fatalf("volume list output %q does not contain %q", listOutput.String(), want)
		}
	}
	if strings.Contains(listOutput.String(), volumeTestRoot(paths)) {
		t.Fatalf("volume list unexpectedly contains a native path: %q", listOutput.String())
	}

	var inspectOutput bytes.Buffer
	inspect := NewRootCommand(Params{
		Registry: emptyRegistry(t),
		Paths:    paths,
		Args:     []string{"volume", "inspect", id[:12]},
		Stdout:   &inspectOutput,
	})
	if err := inspect.Execute(); err != nil {
		t.Fatal(err)
	}
	var yamlInspection volumeInspection
	if err := yaml.Unmarshal(inspectOutput.Bytes(), &yamlInspection); err != nil {
		t.Fatalf("decode YAML inspection: %v\n%s", err, inspectOutput.String())
	}
	if yamlInspection.ID != id ||
		yamlInspection.Type != "home" ||
		yamlInspection.Name != "demo" ||
		yamlInspection.Profile != "work" ||
		yamlInspection.Status != "ready" ||
		yamlInspection.MetadataPath == "" {
		t.Fatalf("YAML inspection = %#v", yamlInspection)
	}

	for _, outputFlag := range []string{"-o", "--output"} {
		var jsonOutput bytes.Buffer
		jsonInspect := NewRootCommand(Params{
			Registry: emptyRegistry(t),
			Paths:    paths,
			Args: []string{
				"volume",
				"inspect",
				id[:12],
				outputFlag,
				"json",
			},
			Stdout: &jsonOutput,
		})
		if err := jsonInspect.Execute(); err != nil {
			t.Fatal(err)
		}
		var jsonInspection volumeInspection
		if err := json.Unmarshal(jsonOutput.Bytes(), &jsonInspection); err != nil {
			t.Fatalf("decode JSON inspection: %v\n%s", err, jsonOutput.String())
		}
		if jsonInspection != yamlInspection {
			t.Fatalf(
				"%s JSON inspection = %#v, want YAML inspection %#v",
				outputFlag,
				jsonInspection,
				yamlInspection,
			)
		}
	}

	var pathOutput bytes.Buffer
	path := NewRootCommand(Params{
		Registry: emptyRegistry(t),
		Paths:    paths,
		Args:     []string{"volume", "path", id[:12]},
		Stdout:   &pathOutput,
	})
	if err := path.Execute(); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(volumeTestRoot(paths), id, "_data") + "\n"
	if pathOutput.String() != wantPath {
		t.Fatalf("volume path output = %q, want %q", pathOutput.String(), wantPath)
	}

	var removeOutput bytes.Buffer
	remove := NewRootCommand(Params{
		Registry: emptyRegistry(t),
		Paths:    paths,
		Args:     []string{"volume", "remove", "--force", id[:12]},
		Stdout:   &removeOutput,
	})
	if err := remove.Execute(); err != nil {
		t.Fatal(err)
	}
	if removeOutput.String() != id+"\n" {
		t.Fatalf("volume remove output = %q, want ID", removeOutput.String())
	}
	if _, err := os.Lstat(filepath.Join(volumeTestRoot(paths), id)); !os.IsNotExist(err) {
		t.Fatalf("volume still exists: %v", err)
	}
}

func TestVolumeMetadataSelectorsCreateFilterPathAndRemove(t *testing.T) {
	paths := config.Paths{XDGDataHome: t.TempDir()}
	run := func(arguments ...string) (string, error) {
		var output bytes.Buffer
		command := NewRootCommand(Params{
			Registry: emptyRegistry(t),
			Paths:    paths,
			Args:     arguments,
			Stdin:    io.NopCloser(strings.NewReader("")),
			Stdout:   &output,
		})
		err := command.Execute()
		return output.String(), err
	}

	defaultOutput, err := run(
		"volume",
		"create",
		"--type",
		"home",
		"--name",
		"migration",
	)
	if err != nil {
		t.Fatal(err)
	}
	defaultID := strings.TrimSpace(defaultOutput)

	workOutput, err := run(
		"volume",
		"create",
		"--type",
		"home",
		"--name",
		"migration",
		"--profile",
		"work",
	)
	if err != nil {
		t.Fatal(err)
	}
	workID := strings.TrimSpace(workOutput)
	if defaultID == workID {
		t.Fatal("profile-distinct homes have the same ID")
	}

	toolOutput, err := run(
		"volume",
		"create",
		"--type",
		"tool",
		"--name",
		"opencode",
		"--purpose",
		"config",
	)
	if err != nil {
		t.Fatal(err)
	}
	toolID := strings.TrimSpace(toolOutput)

	pathOutput, err := run(
		"volume",
		"path",
		"--type",
		"home",
		"--name",
		"migration",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(
		volumeTestRoot(paths),
		defaultID,
		"_data",
	) + "\n"
	if pathOutput != wantPath {
		t.Fatalf("metadata path output = %q, want %q", pathOutput, wantPath)
	}

	listOutput, err := run(
		"volume",
		"list",
		"--type",
		"home",
		"--profile",
		"work",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOutput, workID[:12]) ||
		strings.Contains(listOutput, defaultID[:12]) ||
		strings.Contains(listOutput, toolID[:12]) {
		t.Fatalf("filtered volume list = %q", listOutput)
	}

	inspectOutput, err := run(
		"volume",
		"inspect",
		"--type",
		"tool",
		"--name",
		"opencode",
		"--purpose",
		"config",
	)
	if err != nil {
		t.Fatal(err)
	}
	var inspection volumeInspection
	if err := yaml.Unmarshal([]byte(inspectOutput), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.ID != toolID ||
		inspection.Profile != "default" ||
		inspection.Purpose != "config" {
		t.Fatalf("tool inspection = %#v", inspection)
	}

	if _, err := run(
		"volume",
		"remove",
		"--type",
		"home",
		"--name",
		"migration",
	); err == nil || !strings.Contains(
		err.Error(),
		"requires a terminal",
	) {
		t.Fatalf("noninteractive removal error = %v", err)
	}

	removeOutput, err := run(
		"volume",
		"remove",
		"--force",
		"--type",
		"home",
		"--name",
		"migration",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{defaultID, workID} {
		if !strings.Contains(removeOutput, id+"\n") {
			t.Fatalf("remove output %q does not contain %q", removeOutput, id)
		}
		if _, err := os.Lstat(
			filepath.Join(volumeTestRoot(paths), id),
		); !os.IsNotExist(err) {
			t.Fatalf("filtered volume %q still exists: %v", id, err)
		}
	}
	if _, err := os.Stat(
		filepath.Join(volumeTestRoot(paths), toolID),
	); err != nil {
		t.Fatalf("unmatched tool volume was removed: %v", err)
	}
}

func TestVolumeRemoveByIDAllowsMalformedMetadata(t *testing.T) {
	paths := config.Paths{XDGDataHome: t.TempDir()}
	service, err := storage.NewStore(
		paths,
		storage.DefaultLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	id := strings.Repeat("a", 128)
	object := filepath.Join(volumeTestRoot(paths), id)
	if err := os.MkdirAll(filepath.Join(object, "_data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(object, "metadata.json"),
		[]byte("not json\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	command := NewRootCommand(Params{
		Registry: emptyRegistry(t),
		Paths:    paths,
		Args:     []string{"volume", "remove", "--force", id[:12]},
		Stdout:   &output,
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.String() != id+"\n" {
		t.Fatalf("remove output = %q, want ID", output.String())
	}
	if _, err := os.Lstat(object); !os.IsNotExist(err) {
		t.Fatalf("malformed volume still exists: %v", err)
	}
}

func volumeTestRoot(paths config.Paths) string {
	return filepath.Join(paths.TobyDataDir(), "volumes")
}
