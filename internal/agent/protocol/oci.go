package protocol

// Defines transport-independent OCI progress values.

import "fmt"

// OCISource identifies the preparation component producing diagnostic output.
type OCISource string

const (
	// OCISourceRegistry reports registry transfer output.
	OCISourceRegistry OCISource = "registry"
	// OCISourceExtractor reports rootfs extraction output.
	OCISourceExtractor OCISource = "extractor"
	// OCISourceCache reports image-cache output.
	OCISourceCache OCISource = "cache"
)

func (s OCISource) validate() error {
	switch s {
	case OCISourceRegistry, OCISourceExtractor, OCISourceCache:
		return nil
	default:
		return fmt.Errorf("unknown OCI output source %q", s)
	}
}

// OCIProgressPhase identifies one preparation phase.
type OCIProgressPhase string

const (
	// OCIProgressResolving means image metadata is being resolved.
	OCIProgressResolving OCIProgressPhase = "resolving"
	// OCIProgressDownloading means image content is being downloaded.
	OCIProgressDownloading OCIProgressPhase = "downloading"
	// OCIProgressExtracting means the root filesystem is being extracted.
	OCIProgressExtracting OCIProgressPhase = "extracting"
)

func (p OCIProgressPhase) validate() error {
	switch p {
	case OCIProgressResolving,
		OCIProgressDownloading,
		OCIProgressExtracting:
		return nil
	default:
		return fmt.Errorf("unknown OCI progress phase %q", p)
	}
}

// OCIProgressState is one complete, absolute preparation progress snapshot.
type OCIProgressState struct {
	Phase          OCIProgressPhase `json:"phase"`
	CompletedBytes int64            `json:"completed_bytes"`
	TotalBytes     int64            `json:"total_bytes"`
	CompletedItems int64            `json:"completed_items"`
	TotalItems     int64            `json:"total_items"`
}

func (s OCIProgressState) validate() error {
	if err := s.Phase.validate(); err != nil {
		return err
	}
	if s.CompletedBytes < 0 ||
		s.TotalBytes < 0 ||
		s.CompletedItems < 0 ||
		s.TotalItems < 0 {
		return fmt.Errorf("OCI progress values must not be negative")
	}
	if s.TotalBytes != 0 && s.CompletedBytes > s.TotalBytes {
		return fmt.Errorf("OCI completed bytes exceed total bytes")
	}
	if s.TotalItems != 0 && s.CompletedItems > s.TotalItems {
		return fmt.Errorf("OCI completed items exceed total items")
	}

	return nil
}
