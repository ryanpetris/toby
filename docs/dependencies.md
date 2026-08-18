# Dependency licenses

This is the canonical inventory of every module in the `require` blocks of
[`go.mod`](../go.mod). Update it whenever a direct or indirect requirement is
added, removed, or changed.

Toby accepts only licenses allowed by project policy: permissive licenses and
MPL-2.0. Before accepting a module change, inspect its `LICENSE` or `COPYING`
file and every changed indirect dependency in the Go module cache.

## Direct dependencies

| Module | Version | License |
| --- | --- | --- |
| charm.land/bubbles/v2 | v2.0.0 | MIT |
| charm.land/bubbletea/v2 | v2.0.8 | MIT |
| charm.land/lipgloss/v2 | v2.0.3 | MIT |
| github.com/charmbracelet/x/ansi | v0.11.7 | MIT |
| github.com/charmbracelet/x/vt | v0.0.0-20260305213658-fe36e8c10185 | MIT |
| github.com/creack/pty | v1.1.24 | MIT |
| github.com/distribution/reference | v0.6.0 | Apache-2.0 |
| github.com/evanphx/json-patch/v5 | v5.9.11 | BSD-3-Clause |
| github.com/google/go-containerregistry | v0.21.7 | Apache-2.0 |
| github.com/modelcontextprotocol/go-sdk | v1.2.0 | MIT |
| github.com/muesli/cancelreader | v0.2.2 | MIT |
| github.com/opencontainers/go-digest | v1.0.0 | Apache-2.0 |
| github.com/opencontainers/image-spec | v1.1.1 | Apache-2.0 |
| github.com/opencontainers/runtime-spec | v1.2.1 | Apache-2.0 |
| github.com/opencontainers/umoci | v0.6.0 | Apache-2.0 |
| github.com/pelletier/go-toml/v2 | v2.3.1 | MIT |
| github.com/spf13/cobra | v1.10.2 | Apache-2.0 |
| go.uber.org/dig | v1.19.0 | MIT |
| go.uber.org/fx | v1.24.0 | MIT |
| golang.org/x/crypto | v0.52.0 | BSD-3-Clause |
| golang.org/x/sys | v0.46.0 | BSD-3-Clause |
| golang.org/x/term | v0.43.0 | BSD-3-Clause |
| google.golang.org/grpc | v1.82.1 | Apache-2.0 |
| google.golang.org/protobuf | v1.36.11 | BSD-3-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | MIT and Apache-2.0 |

## Indirect dependencies

| Module | Version | License |
| --- | --- | --- |
| github.com/AdaLogics/go-fuzz-headers | v0.0.0-20230106234847-43070de90fa1 | Apache-2.0 |
| github.com/apex/log | v1.9.0 | MIT |
| github.com/blang/semver/v4 | v4.0.0 | MIT |
| github.com/charmbracelet/colorprofile | v0.4.3 | MIT |
| github.com/charmbracelet/ultraviolet | v0.0.0-20260703014108-f5a850f9c2b7 | MIT |
| github.com/charmbracelet/x/exp/ordered | v0.1.0 | MIT |
| github.com/charmbracelet/x/term | v0.2.2 | MIT |
| github.com/charmbracelet/x/termios | v0.1.1 | MIT |
| github.com/charmbracelet/x/windows | v0.2.2 | MIT |
| github.com/clipperhouse/displaywidth | v0.11.0 | MIT |
| github.com/clipperhouse/uax29/v2 | v2.7.0 | MIT |
| github.com/cyphar/filepath-securejoin | v0.5.0 | BSD-3-Clause and MPL-2.0 |
| github.com/docker/cli | v29.5.3+incompatible | Apache-2.0 |
| github.com/docker/docker-credential-helpers | v0.9.3 | MIT |
| github.com/google/jsonschema-go | v0.3.0 | MIT |
| github.com/inconshreveable/mousetrap | v1.1.0 | Apache-2.0 |
| github.com/klauspost/compress | v1.18.6 | BSD-3-Clause |
| github.com/klauspost/pgzip | v1.2.6 | MIT |
| github.com/kr/pretty | v0.3.1 | MIT |
| github.com/lucasb-eyer/go-colorful | v1.4.0 | MIT |
| github.com/mattn/go-runewidth | v0.0.23 | MIT |
| github.com/moby/sys/user | v0.4.0 | Apache-2.0 |
| github.com/moby/sys/userns | v0.1.0 | Apache-2.0 |
| github.com/pkg/errors | v0.9.1 | BSD-2-Clause |
| github.com/rivo/uniseg | v0.4.7 | MIT |
| github.com/rogpeppe/go-internal | v1.14.1 | BSD-3-Clause |
| github.com/rootless-containers/proto/go-proto | v0.0.0-20230421021042-4cd87ebadd67 | Apache-2.0 |
| github.com/sirupsen/logrus | v1.9.4 | MIT |
| github.com/spf13/pflag | v1.0.10 | BSD-3-Clause |
| github.com/vbatts/go-mtree | v0.6.1-0.20250911112631-8307d76bc1b9 | BSD-3-Clause |
| github.com/xo/terminfo | v0.0.0-20220910002029-abceb7e1c41e | MIT |
| github.com/yosida95/uritemplate/v3 | v3.0.2 | BSD-3-Clause |
| go.uber.org/multierr | v1.10.0 | MIT |
| go.uber.org/zap | v1.26.0 | MIT |
| golang.org/x/net | v0.54.0 | BSD-3-Clause |
| golang.org/x/oauth2 | v0.36.0 | BSD-3-Clause |
| golang.org/x/sync | v0.22.0 | BSD-3-Clause |
| golang.org/x/text | v0.37.0 | BSD-3-Clause |
| google.golang.org/genproto/googleapis/rpc | v0.0.0-20260414002931-afd174a4e478 | Apache-2.0 |
| gopkg.in/check.v1 | v1.0.0-20201130134442-10cb98267c6c | BSD-2-Clause |
| gotest.tools/v3 | v3.5.2 | Apache-2.0 |
