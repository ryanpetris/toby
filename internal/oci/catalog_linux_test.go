//go:build linux

package oci

// Exercises reference/object catalog views and Docker-style untag semantics.

import (
	"errors"
	"testing"

	"petris.dev/toby/internal/oci/image"
)

func TestImageCatalogSeparatesReferencesFromSharedObject(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})

	for _, reference := range []string{"example:one", "example:two"} {
		prepared, err := service.Prepare(t.Context(), Request{
			Reference:  reference,
			Platform:   testPlatform(),
			PullPolicy: image.PullAlways,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}

	images, err := service.ListImages(t.Context(), ImageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("ListImages() returned %d entries, want 2", len(images))
	}
	if images[0].Reference == images[1].Reference ||
		images[0].ObjectKey != images[1].ObjectKey ||
		images[0].Manifest.Digest != images[1].Manifest.Digest {
		t.Fatalf("catalog entries do not share one object: %#v", images)
	}
	if len(images[0].References) != 2 ||
		len(images[1].References) != 2 {
		t.Fatalf("object references = %#v, %#v", images[0].References, images[1].References)
	}

	inspected, err := service.InspectImage(
		t.Context(),
		"example:one",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Reference != "docker.io/library/example:one" ||
		inspected.RootfsPath == "" ||
		inspected.Problem != "" {
		t.Fatalf("InspectImage() = %#v", inspected)
	}
}

func TestImageCatalogFiltersReferencePlatformDigestAndDangling(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	prepared, err := service.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := service.InspectImage(
		t.Context(),
		"example:latest",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	notDangling := false
	images, err := service.ListImages(t.Context(), ImageFilter{
		Reference: "example",
		Platform:  testPlatform(),
		Digest:    info.Manifest.Digest,
		Dangling:  &notDangling,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].ID != info.ID {
		t.Fatalf("filtered images = %#v", images)
	}

	wrong := testPlatform()
	wrong.Architecture = "arm64"
	images, err = service.ListImages(t.Context(), ImageFilter{
		Platform: wrong,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Fatalf("wrong-platform images = %#v", images)
	}
}

func TestImageInspectionFallsBackFromUnknownHexIDToReference(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	const reference = "deadbeefdead"
	prepared, err := service.Prepare(t.Context(), Request{
		Reference:  reference,
		Platform:   testPlatform(),
		PullPolicy: image.PullIfMissing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := service.InspectImage(
		t.Context(),
		reference,
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if info.Reference != "docker.io/library/deadbeefdead:latest" {
		t.Fatalf("reference = %q", info.Reference)
	}
}

func TestImageDigestInspectionUsesSelectedPlatform(t *testing.T) {
	service := newTestStore(
		t,
		&fakePipeline{platformAgnostic: true},
	)
	platforms := []string{"amd64", "arm64"}
	var manifest string
	for _, architecture := range platforms {
		platform := testPlatform()
		platform.Architecture = architecture
		prepared, err := service.Prepare(t.Context(), Request{
			Reference:  "example:latest",
			Platform:   platform,
			PullPolicy: image.PullAlways,
		})
		if err != nil {
			t.Fatal(err)
		}
		if manifest == "" {
			manifest = prepared.Spec().Manifest.Digest.String()
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}

	for _, architecture := range platforms {
		platform := testPlatform()
		platform.Architecture = architecture
		info, err := service.InspectImage(
			t.Context(),
			manifest,
			platform,
		)
		if err != nil {
			t.Fatal(err)
		}
		if info.Platform.Architecture != architecture {
			t.Fatalf(
				"platform = %#v, want architecture %q",
				info.Platform,
				architecture,
			)
		}
	}
}

func TestImageRemovalUntagsSharedObject(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	for _, reference := range []string{"example:one", "example:two"} {
		prepared, err := service.Prepare(t.Context(), Request{
			Reference:  reference,
			Platform:   testPlatform(),
			PullPolicy: image.PullAlways,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}

	first, err := service.InspectImage(
		t.Context(),
		"example:one",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var progress []ImageRemovalProgress
	removed, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{first},
		false,
		func(event ImageRemovalProgress) {
			progress = append(progress, event)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 ||
		len(progress) != 2 ||
		progress[1].Phase != ImageRemovalPhaseUntagged {
		t.Fatalf("removed = %#v, progress = %#v", removed, progress)
	}

	images, err := service.ListImages(t.Context(), ImageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 ||
		images[0].Reference != "docker.io/library/example:two" {
		t.Fatalf("remaining images = %#v", images)
	}
}

func TestForcedImageRemovalLeavesBusyObjectDangling(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	prepared, err := service.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err != nil {
		t.Fatal(err)
	}

	reference, err := service.InspectImage(
		t.Context(),
		"example:latest",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{reference},
		false,
		nil,
	); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("RemoveImages() error = %v, want ErrImageBusy", err)
	}

	removed, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{reference},
		true,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("RemoveImages() removed %d entries", len(removed))
	}

	dangling := true
	images, err := service.ListImages(t.Context(), ImageFilter{
		Dangling: &dangling,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || !images[0].Dangling() {
		t.Fatalf("dangling images = %#v", images)
	}
	if _, err := service.Prepare(t.Context(), Request{
		Reference:  "example:latest",
		Platform:   testPlatform(),
		PullPolicy: image.PullNever,
	}); err == nil {
		t.Fatal("PullNever unexpectedly found removed reference")
	}

	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveImages(
		t.Context(),
		images,
		false,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	images, err = service.ListImages(t.Context(), ImageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Fatalf("images after dangling removal = %#v", images)
	}
}

func TestImageRemovalDeletesFinalUnusedReferenceAndObject(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	prepared, err := service.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	reference, err := service.InspectImage(
		t.Context(),
		"example:latest",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{reference},
		false,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %#v", removed)
	}
	images, err := service.ListImages(t.Context(), ImageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Fatalf("remaining images = %#v", images)
	}
}

func TestForcedObjectRemovalRemovesAliases(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	for _, reference := range []string{"example:one", "example:two"} {
		prepared, err := service.Prepare(t.Context(), Request{
			Reference:  reference,
			Platform:   testPlatform(),
			PullPolicy: image.PullAlways,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}

	reference, err := service.InspectImage(
		t.Context(),
		"example:one",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	object, err := service.InspectImage(
		t.Context(),
		reference.Manifest.Digest.String(),
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{object},
		false,
		nil,
	); err == nil {
		t.Fatal("referenced object removal unexpectedly succeeded")
	}
	if _, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{object},
		true,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	images, err := service.ListImages(t.Context(), ImageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Fatalf("remaining images = %#v", images)
	}
}

func TestForcedDanglingObjectRemovalStillRejectsBusyObject(t *testing.T) {
	service := newTestStore(t, &fakePipeline{})
	prepared, err := service.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err != nil {
		t.Fatal(err)
	}

	reference, err := service.InspectImage(
		t.Context(),
		"example:latest",
		testPlatform(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveImages(
		t.Context(),
		[]ImageInfo{reference},
		true,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	dangling := true
	objects, err := service.ListImages(t.Context(), ImageFilter{
		Dangling: &dangling,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("dangling objects = %#v", objects)
	}
	if _, err := service.RemoveImages(
		t.Context(),
		objects,
		true,
		nil,
	); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("RemoveImages() error = %v, want ErrImageBusy", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImageRemovalResultReportsPartialReferenceMutation(t *testing.T) {
	reference := ImageInfo{
		ID:        "reference",
		Kind:      ImageEntryReference,
		ObjectKey: "object",
	}
	object := ImageInfo{
		ID:         "object",
		Kind:       ImageEntryObject,
		ObjectKey:  "object",
		References: []string{"example:latest"},
	}
	var progress []ImageRemovalProgress
	removed := reportImageRemovalResults(
		[]ImageInfo{reference, object},
		map[string]bool{"reference": true},
		nil,
		true,
		func(event ImageRemovalProgress) {
			progress = append(progress, event)
		},
	)
	if len(removed) != 1 || removed[0].ID != reference.ID {
		t.Fatalf("removed = %#v", removed)
	}
	if len(progress) != 2 ||
		progress[0].Phase != ImageRemovalPhaseUntagged ||
		progress[1].Phase != ImageRemovalPhaseFailed {
		t.Fatalf("progress = %#v", progress)
	}
}
