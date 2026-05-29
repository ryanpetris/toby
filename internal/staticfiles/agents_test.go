package staticfiles

import (
	"bytes"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestAgentFilesExposeSharedGuidance(t *testing.T) {
	service := testService(t)
	builder := service.NewBuilder()
	if err := RegisterAgentFiles(builder); err != nil {
		t.Fatal(err)
	}
	files := builder.Files()
	if len(files) != 1 || files[0].Path != GitAgentsPath {
		t.Fatalf("files = %#v, want git guidance only", files)
	}
	if !bytes.Contains(files[0].Data, []byte("Toby Git")) {
		t.Fatalf("git guidance missing")
	}
}

func testService(t *testing.T) *Service {
	t.Helper()
	var service *Service
	app := fxtest.New(t,
		fx.Provide(NewService),
		fx.Populate(&service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	return service
}
