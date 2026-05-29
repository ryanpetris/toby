package staticfiles

import (
	"bytes"
	"context"
	"testing"

	"petris.dev/toby/fusekit"
	"petris.dev/toby/internal/staticmount"
)

func TestAgentFilesExposeSharedGuidance(t *testing.T) {
	service := NewService()
	builder := service.NewBuilder()
	if err := RegisterAgentFiles(builder, false); err != nil {
		t.Fatal(err)
	}
	files := builder.Files()
	if len(files) != 1 || files[0].Path != GitAgentsPath {
		t.Fatalf("files = %#v, want git guidance only", files)
	}
	if len(files[0].Data) != 0 || files[0].Source == nil {
		t.Fatalf("file = %#v, want source-backed file", files[0])
	}
	if !bytes.Contains(readStaticFile(t, files, GitAgentsPath), []byte("Toby Git")) {
		t.Fatalf("git guidance missing")
	}

	builder = service.NewBuilder()
	if err := RegisterAgentFiles(builder, true); err != nil {
		t.Fatal(err)
	}
	files = builder.Files()
	if len(files) != 2 || files[1].Path != ProjectMountAgentsPath {
		t.Fatalf("files = %#v, want project mount guidance", files)
	}
	if !bytes.Contains(readStaticFile(t, files, ProjectMountAgentsPath), []byte("Toby Project Mounts")) {
		t.Fatalf("project mount guidance missing")
	}
}

func readStaticFile(t *testing.T, files []staticmount.File, path string) []byte {
	t.Helper()
	mount, err := staticmount.New("static", "/toby/static", files)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpOpen, Path: "/toby/static/" + path})
	if err != nil {
		t.Fatal(err)
	}
	data, err := res.Handle.(fusekit.FileReader).Read(context.Background(), make([]byte, 4096), 0)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
