package oci

// Normalizes registry image references for transfer, cache identity, display,
// and immutable metadata.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

type normalizedRequest struct {
	reference  string
	repository string
	platform   ocispec.Platform
	key        string
}

type referenceKeyDocument struct {
	Reference string           `json:"reference"`
	Platform  ocispec.Platform `json:"platform"`
}

func normalizeRequest(request Request) (normalizedRequest, error) {
	reference, err := image.NormalizeReference(request.Reference)
	if err != nil {
		return normalizedRequest{}, err
	}
	repository, err := image.Repository(reference)
	if err != nil {
		return normalizedRequest{}, err
	}
	platform, err := image.NormalizePlatform(request.Platform)
	if err != nil {
		return normalizedRequest{}, err
	}

	document := referenceKeyDocument{
		Reference: reference,
		Platform:  platform,
	}
	data, err := json.Marshal(document)
	if err != nil {
		return normalizedRequest{}, fmt.Errorf(
			"encode OCI image cache identity: %w",
			err,
		)
	}
	sum := sha256.Sum256(data)

	return normalizedRequest{
		reference:  reference,
		repository: repository,
		platform:   platform,
		key:        hex.EncodeToString(sum[:]),
	}, nil
}
