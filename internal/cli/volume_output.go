package cli

// Serializes volume inspection documents and redirected list tables.

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"petris.dev/toby/internal/storage"
)

func writeVolumeList(
	output io.Writer,
	volumes []storage.VolumeInfo,
) error {
	if terminalStream(output) {
		return writeVolumeBubbleTable(output, volumes)
	}

	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(
		table,
		"ID\tTYPE\tNAME\tPROFILE\tPURPOSE\tSTATUS",
	); err != nil {
		return err
	}
	for _, volume := range volumes {
		status := "ready"
		if volume.Problem != "" {
			status = "invalid"
		}
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			volume.ShortID(),
			tableCell(string(volume.Type)),
			tableCell(volume.Name),
			tableCell(volume.Profile),
			tableCell(volume.Purpose),
			status,
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeVolumeInspection(
	output io.Writer,
	volume storage.VolumeInfo,
	outputFormat string,
) error {
	status := "ready"
	if volume.Problem != "" {
		status = "invalid"
	}
	document := volumeInspection{
		ID:           volume.ID,
		Type:         string(volume.Type),
		Name:         volume.Name,
		Profile:      volume.Profile,
		Purpose:      volume.Purpose,
		Status:       status,
		Path:         volume.DataPath,
		ObjectPath:   volume.ObjectPath,
		MetadataPath: volume.MetadataPath,
		Problem:      volume.Problem,
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
			"unsupported volume inspection output format %q",
			outputFormat,
		)
	}
	if err != nil {
		return fmt.Errorf(
			"encode volume inspection as %s: %w",
			outputFormat,
			err,
		)
	}
	_, err = output.Write(data)
	return err
}

type volumeInspection struct {
	ID           string `json:"id" yaml:"id"`
	Type         string `json:"type" yaml:"type"`
	Name         string `json:"name" yaml:"name"`
	Profile      string `json:"profile" yaml:"profile"`
	Purpose      string `json:"purpose" yaml:"purpose"`
	Status       string `json:"status" yaml:"status"`
	Path         string `json:"path" yaml:"path"`
	ObjectPath   string `json:"object_path" yaml:"object_path"`
	MetadataPath string `json:"metadata_path" yaml:"metadata_path"`
	Problem      string `json:"problem,omitempty" yaml:"problem,omitempty"`
}
