package cli

// Serializes image inspection documents and redirected list tables.

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"petris.dev/toby/internal/oci"
)

func writeImageList(
	output io.Writer,
	images []oci.ImageInfo,
) error {
	if terminalStream(output) {
		return writeImageBubbleTable(output, images)
	}

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(
		table,
		"ID\tREFERENCE\tPLATFORM\tIMAGE ID\tSTATUS",
	); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\n",
			tableCell(image.ShortID()),
			tableCell(image.Reference),
			tableCell(imagePlatformString(image)),
			tableCell(image.ImageID()),
			imageStatus(image),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeImageInspection(
	output io.Writer,
	image oci.ImageInfo,
	outputFormat string,
) error {
	document := imageInspection{
		ID:            image.ID,
		Kind:          string(image.Kind),
		Reference:     image.Reference,
		Platform:      imagePlatformString(image),
		ImageDigest:   image.Manifest.Digest.String(),
		ConfigDigest:  image.Config.Digest.String(),
		References:    image.References,
		Status:        imageStatus(image),
		Path:          image.RootfsPath,
		ReferencePath: image.ReferencePath,
		ObjectPath:    image.ObjectPath,
		MetadataPath:  image.MetadataPath,
		ObjectKey:     image.ObjectKey,
		Runtime: imageRuntimeInspection{
			Environment: image.Runtime.Environment,
			Workdir:     image.Runtime.Workdir,
			Entrypoint:  image.Runtime.Entrypoint,
			Command:     image.Runtime.Command,
			User:        image.Runtime.User,
			Labels:      image.Runtime.Labels,
		},
		Problem: image.Problem,
	}

	var data []byte
	var err error
	switch outputFormat {
	case "yaml":
		data, err = yaml.Marshal(document)
	case "json":
		data, err = json.MarshalIndent(document, "", "  ")
		if err == nil {
			data = append(data, '\n')
		}
	default:
		return fmt.Errorf(
			"unsupported image inspection output format %q",
			outputFormat,
		)
	}
	if err != nil {
		return fmt.Errorf(
			"encode image inspection as %s: %w",
			outputFormat,
			err,
		)
	}
	_, err = output.Write(data)
	return err
}

func imageStatus(image oci.ImageInfo) string {
	if image.Problem != "" {
		return "invalid"
	}
	if image.Dangling() {
		return "dangling"
	}
	return "ready"
}

func imagePlatformString(image oci.ImageInfo) string {
	platform := image.Platform
	if platform.OS == "" && platform.Architecture == "" {
		return "-"
	}
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}
	return value
}

type imageInspection struct {
	ID            string                 `json:"id" yaml:"id"`
	Kind          string                 `json:"kind" yaml:"kind"`
	Reference     string                 `json:"reference,omitempty" yaml:"reference,omitempty"`
	Platform      string                 `json:"platform" yaml:"platform"`
	ImageDigest   string                 `json:"image_digest,omitempty" yaml:"image_digest,omitempty"`
	ConfigDigest  string                 `json:"config_digest,omitempty" yaml:"config_digest,omitempty"`
	References    []string               `json:"references,omitempty" yaml:"references,omitempty"`
	Status        string                 `json:"status" yaml:"status"`
	Path          string                 `json:"path,omitempty" yaml:"path,omitempty"`
	ReferencePath string                 `json:"reference_path,omitempty" yaml:"reference_path,omitempty"`
	ObjectPath    string                 `json:"object_path,omitempty" yaml:"object_path,omitempty"`
	MetadataPath  string                 `json:"metadata_path,omitempty" yaml:"metadata_path,omitempty"`
	ObjectKey     string                 `json:"object_key,omitempty" yaml:"object_key,omitempty"`
	Runtime       imageRuntimeInspection `json:"runtime" yaml:"runtime"`
	Problem       string                 `json:"problem,omitempty" yaml:"problem,omitempty"`
}

type imageRuntimeInspection struct {
	Environment []string          `json:"environment,omitempty" yaml:"environment,omitempty"`
	Workdir     string            `json:"workdir,omitempty" yaml:"workdir,omitempty"`
	Entrypoint  []string          `json:"entrypoint,omitempty" yaml:"entrypoint,omitempty"`
	Command     []string          `json:"command,omitempty" yaml:"command,omitempty"`
	User        string            `json:"user,omitempty" yaml:"user,omitempty"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}
