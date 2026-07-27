//go:build linux

package bwrap

// Exercises the read-only background-service policy, deterministic rendering,
// and exact descriptor validation.

import (
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestBackgroundServicePlanCanonicalIsDetachedAndDeterministic(
	t *testing.T,
) {
	first := validBackgroundServicePlan()
	second := first.Clone()
	slices.Reverse(second.Binds)
	slices.Reverse(second.Environment)

	gotFirst := first.Canonical()
	gotSecond := second.Canonical()
	if !reflect.DeepEqual(gotFirst, gotSecond) {
		t.Fatalf(
			"canonical background-service plans differ:\n%#v\n%#v",
			gotFirst,
			gotSecond,
		)
	}

	first.Binds[0].HostPath = "/mutated"
	first.Environment[0].Value = "mutated"
	first.Command[0] = "mutated"
	first.Runtime.HostPath = "/mutated-runtime"
	if gotFirst.Binds[0].HostPath == "/mutated" ||
		gotFirst.Environment[0].Value == "mutated" ||
		gotFirst.Command[0] == "mutated" ||
		gotFirst.Runtime.HostPath == "/mutated-runtime" {
		t.Fatal("canonical background-service plan aliases input data")
	}
}

func TestBackgroundServicePlanValidatesReadOnlyPathGraphs(t *testing.T) {
	valid := validBackgroundServicePlan()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	withoutRuntime := valid.Clone()
	withoutRuntime.Runtime = nil
	if err := withoutRuntime.Validate(); err == nil {
		t.Fatal("runtime workdir without a runtime was accepted")
	}
	withoutRuntime.Workdir = "/"
	if err := withoutRuntime.Validate(); err != nil {
		t.Fatalf("plan without a writable runtime: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*BackgroundServicePlan)
	}{
		{
			name: "invalid id",
			mutate: func(plan *BackgroundServicePlan) {
				plan.ID = "../service"
			},
		},
		{
			name: "invalid digest",
			mutate: func(plan *BackgroundServicePlan) {
				plan.RootFS.Digest = "latest"
			},
		},
		{
			name: "relative rootfs",
			mutate: func(plan *BackgroundServicePlan) {
				plan.RootFS.Path = "rootfs"
			},
		},
		{
			name: "relative workdir",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Workdir = "service"
			},
		},
		{
			name: "negative identity",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Identity.HostUID = -1
			},
		},
		{
			name: "invalid network",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Network = "internet"
			},
		},
		{
			name: "empty command",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Command = nil
			},
		},
		{
			name: "command NUL",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Command = []string{"/bin/service", "bad\x00argument"}
			},
		},
		{
			name: "reserved environment",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Environment = append(
					plan.Environment,
					EnvironmentVariable{Name: "HOME", Value: "/root"},
				)
			},
		},
		{
			name: "duplicate environment",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Environment = append(
					plan.Environment,
					EnvironmentVariable{Name: "A_FIRST", Value: "duplicate"},
				)
			},
		},
		{
			name: "writable ordinary bind",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Access = mount.AccessRegular
			},
		},
		{
			name: "optional bind",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Optional = true
			},
		},
		{
			name: "auth socket device access",
			mutate: func(plan *BackgroundServicePlan) {
				for index := range plan.Binds {
					if plan.Binds[index].Target ==
						BackgroundServiceAuthSocketTarget {
						plan.Binds[index].Access = mount.AccessDev
					}
				}
			},
		},
		{
			name: "device access outside auth socket",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Access = mount.AccessDev
			},
		},
		{
			name: "reserved proc target",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Target = "/proc/service"
			},
		},
		{
			name: "reserved dev target",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Target = "/dev"
			},
		},
		{
			name: "reserved tmp target",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Target = "/tmp/config"
			},
		},
		{
			name: "reserved run target",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].Target = "/run/other"
			},
		},
		{
			name: "overlapping bind targets",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[1].Target = plan.Binds[0].Target + "/nested"
			},
		},
		{
			name: "wrong runtime target",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Runtime.Target = "/run/toby/other"
			},
		},
		{
			name: "read-only runtime",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Runtime.Access = mount.AccessReadOnly
			},
		},
		{
			name: "rootfs aliases bind path",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].HostPath = plan.RootFS.Path
			},
		},
		{
			name: "rootfs contains bind path",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[0].HostPath = plan.RootFS.Path + "/etc"
			},
		},
		{
			name: "bind host paths overlap",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Binds[1].HostPath =
					plan.Binds[0].HostPath + "/nested"
			},
		},
		{
			name: "runtime host path overlaps bind",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Runtime.HostPath =
					plan.Binds[0].HostPath + "/runtime"
			},
		},
		{
			name: "runtime host path overlaps rootfs",
			mutate: func(plan *BackgroundServicePlan) {
				plan.Runtime.HostPath = plan.RootFS.Path + "/runtime"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validBackgroundServicePlan()
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("invalid background-service plan was accepted")
			}
		})
	}
}

func TestRenderBackgroundServiceGoldenPolicyAndDescriptors(t *testing.T) {
	plan := validBackgroundServicePlan()
	plan.Environment[0].Value = "environment-secret-sentinel"
	plan.Command = append(plan.Command, "public-command-sentinel")
	sources := validBackgroundServiceSources(t, plan)

	invocation, err := RenderBackgroundService(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	if got, want := invocation.Args, []string{
		"--args", "8",
		"--",
		"/usr/bin/service", "serve", "public-command-sentinel",
	}; !slices.Equal(got, want) {
		t.Fatalf("public args = %q, want %q", got, want)
	}
	public := strings.Join(invocation.Args, "\x00")
	if strings.Contains(public, "environment-secret-sentinel") {
		t.Fatalf(
			"public args expose environment secret: %q",
			invocation.Args,
		)
	}
	if !strings.Contains(public, "public-command-sentinel") {
		t.Fatalf("public args omit the fixed command: %q", invocation.Args)
	}

	args, err := invocationArguments(invocation)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"--unshare-user",
		"--uid", strconv.Itoa(plan.Identity.HostUID),
		"--gid", strconv.Itoa(plan.Identity.HostGID),
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--cap-drop", "ALL",
		"--overlay-src", "/proc/self/fd/3",
		"--tmp-overlay", "/",
		"--ro-bind-fd", "3", "/dev",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--dir", "/run/toby",
		"--chmod", "0700", "/run/toby",
		"--ro-bind-fd", "4", "/etc/resolv.conf",
		"--ro-bind-fd", "5", "/etc/ssl/certs",
		"--ro-bind-fd", "6", "/run/toby/auth.sock",
		"--bind-fd", "7", "/run/toby/service",
		"--remount-ro", "/",
		"--clearenv",
		"--setenv", "HOME", "/tmp",
		"--setenv", "TOBY_SANDBOX", "1",
		"--setenv", "A_FIRST", "first",
		"--setenv", "Z_LAST", "environment-secret-sentinel",
		"--chdir", "/run/toby/service",
		"--",
		"/usr/bin/service", "serve", "public-command-sentinel",
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf(
			"rendered arguments differ:\n got: %q\nwant: %q",
			args,
			wantArgs,
		)
	}
	for _, forbidden := range []string{
		"--overlay",
		"/toby/home",
		"/toby/workspace",
	} {
		if slices.Contains(args, forbidden) {
			t.Fatalf("read-only service args contain %q: %q", forbidden, args)
		}
	}
	if invocation.Mode != ExecutionNonInteractive {
		t.Fatalf("mode = %q, want noninteractive", invocation.Mode)
	}
	if err := validateBackgroundInvocation(invocation, ProcessIO{}); err != nil {
		t.Fatalf("rendered invocation is not background-safe: %v", err)
	}

	canonical := plan.Canonical()
	wantSources := []*os.File{sources.RootFS}
	for _, bind := range canonical.Binds {
		wantSources = append(wantSources, sources.Binds[bind.Target])
	}
	wantSources = append(wantSources, sources.Runtime)
	if got, want := len(invocation.ExtraFiles), len(wantSources)+1; got != want {
		t.Fatalf("extra files = %d, want %d", got, want)
	}
	for index, source := range wantSources {
		assertSameDescriptorObject(
			t,
			invocation.ExtraFiles[index],
			source,
			index+childExtraFileBaseFD,
		)
	}
}

func TestRenderBackgroundServiceSupportsPrivateNetwork(t *testing.T) {
	plan := validBackgroundServicePlan()
	plan.Network = NetworkPrivate

	invocation, err := RenderBackgroundService(
		plan,
		validBackgroundServiceSources(t, plan),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	args, err := invocationArguments(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--unshare-net") {
		t.Fatalf("private-network service omits --unshare-net: %q", args)
	}
}

func TestRenderBackgroundServiceWithoutWritableRuntime(t *testing.T) {
	plan := validBackgroundServicePlan()
	plan.Runtime = nil

	invocation, err := RenderBackgroundService(
		plan,
		validBackgroundServiceSources(t, plan),
	)
	if invocation != nil {
		unexpected := invocation
		t.Cleanup(func() {
			if err := unexpected.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), "requires the writable runtime") {
		t.Fatalf("runtime workdir error = %v, want required runtime", err)
	}

	plan.Workdir = "/"

	invocation, err = RenderBackgroundService(
		plan,
		validBackgroundServiceSources(t, plan),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	args, err := invocationArguments(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, BackgroundServiceRuntimeTarget) {
		t.Fatalf(
			"service without runtime mounts the writable target: %q",
			args,
		)
	}
	if got, want := len(invocation.ExtraFiles), 5; got != want {
		t.Fatalf("extra files = %d, want %d", got, want)
	}
}

func TestRenderBackgroundServiceRejectsInvalidSourceSets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BackgroundServicePlan, *BackgroundServiceSources)
		match  string
	}{
		{
			name: "missing bind with replacement",
			mutate: func(
				plan *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				delete(sources.Binds, plan.Binds[0].Target)
				sources.Binds["/unexpected"] = openDirectorySource(t)
			},
			match: "missing background-service bind source",
		},
		{
			name: "unexpected bind count",
			mutate: func(
				_ *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Binds["/unexpected"] = openDirectorySource(t)
			},
			match: "descriptor count",
		},
		{
			name: "rootfs is regular file",
			mutate: func(
				_ *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.RootFS = openExecutableSource(t)
			},
			match: "rootfs source must be a directory descriptor",
		},
		{
			name: "auth capability is directory",
			mutate: func(
				_ *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Binds[BackgroundServiceAuthSocketTarget] =
					openDirectorySource(t)
			},
			match: "device-access source must be a Unix socket descriptor",
		},
		{
			name: "read-only capability is socket",
			mutate: func(
				plan *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Binds[plan.Binds[0].Target] =
					openSocketSource(t)
			},
			match: "read-only source must be a directory or regular-file descriptor",
		},
		{
			name: "runtime is regular file",
			mutate: func(
				_ *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Runtime = openExecutableSource(t)
			},
			match: "runtime source must be a directory descriptor",
		},
		{
			name: "rootfs aliases bind",
			mutate: func(
				plan *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Binds[plan.Binds[0].Target] = sources.RootFS
			},
			match: "alias the same host object",
		},
		{
			name: "binds alias",
			mutate: func(
				plan *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				sources.Binds[plan.Binds[2].Target] =
					sources.Binds[plan.Binds[0].Target]
			},
			match: "alias the same host object",
		},
		{
			name: "runtime aliases rootfs",
			mutate: func(
				_ *BackgroundServicePlan,
				sources *BackgroundServiceSources,
			) {
				setBackgroundServiceSourceMode(
					t,
					sources.RootFS,
					0o700,
				)
				sources.Runtime = sources.RootFS
			},
			match: "alias the same host object",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validBackgroundServicePlan()
			sources := validBackgroundServiceSources(t, plan)
			test.mutate(&plan, &sources)

			invocation, err := RenderBackgroundService(plan, sources)
			if invocation != nil {
				t.Cleanup(func() {
					if err := invocation.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v, want text %q", err, test.match)
			}
		})
	}
}

func TestRenderBackgroundServiceRejectsOversizedDescriptorSet(t *testing.T) {
	plan := validBackgroundServicePlan()
	plan.Binds = make(
		[]mount.Bind,
		maxBackgroundServiceSourceDescriptors,
	)
	if err := plan.Validate(); err == nil ||
		!strings.Contains(err.Error(), "descriptor count exceeds") {
		t.Fatalf("Validate error = %v, want descriptor limit", err)
	}

	invocation, err := RenderBackgroundService(
		plan,
		BackgroundServiceSources{},
	)
	if invocation != nil {
		t.Cleanup(func() {
			if err := invocation.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), "descriptor count exceeds") {
		t.Fatalf("error = %v, want descriptor limit", err)
	}
}

func validBackgroundServicePlan() BackgroundServicePlan {
	return BackgroundServicePlan{
		ID: "models-gateway",
		RootFS: RootFS{
			Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Path:   "/store/rootfs",
		},
		Binds: []mount.Bind{
			{
				HostPath: "/host/ca",
				Target:   "/etc/ssl/certs",
				Access:   mount.AccessReadOnly,
			},
			{
				HostPath: "/host/auth/auth.sock",
				Target:   BackgroundServiceAuthSocketTarget,
				Access:   mount.AccessReadOnly,
			},
			{
				HostPath: "/host/dns/resolv.conf",
				Target:   "/etc/resolv.conf",
				Access:   mount.AccessReadOnly,
			},
		},
		Runtime: &RuntimeAsset{
			HostPath: "/host/runtime/service",
			Target:   BackgroundServiceRuntimeTarget,
			Access:   mount.AccessRegular,
		},
		Workdir: BackgroundServiceRuntimeTarget,
		Environment: []EnvironmentVariable{
			{Name: "Z_LAST", Value: "last"},
			{Name: "A_FIRST", Value: "first"},
		},
		Identity: Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
		Network: NetworkHost,
		Command: []string{"/usr/bin/service", "serve"},
	}
}

func validBackgroundServiceSources(
	t *testing.T,
	plan BackgroundServicePlan,
) BackgroundServiceSources {
	t.Helper()

	sources := BackgroundServiceSources{
		RootFS: openDirectorySource(t),
		Binds:  make(map[string]*os.File, len(plan.Binds)),
	}
	for _, bind := range plan.Binds {
		if bind.Target == BackgroundServiceAuthSocketTarget {
			source := openSocketSource(t)
			setBackgroundServiceSourceMode(t, source, 0o600)
			sources.Binds[bind.Target] = source
			continue
		}
		sources.Binds[bind.Target] = openDirectorySource(t)
	}
	if plan.Runtime != nil {
		sources.Runtime = openDirectorySource(t)
		setBackgroundServiceSourceMode(t, sources.Runtime, 0o700)
	}

	return sources
}

func setBackgroundServiceSourceMode(
	t *testing.T,
	source *os.File,
	mode os.FileMode,
) {
	t.Helper()

	name, err := os.Readlink(
		"/proc/self/fd/" +
			strconv.FormatUint(uint64(source.Fd()), 10),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}
