# Dependency licenses

Inventory of every Go module Toby depends on (the `require` blocks in
[`go.mod`](../go.mod)) and the license each one ships under. Keep this file in
sync with `go.mod`: whenever you add, remove, or update a dependency — direct or
indirect — update the matching row here in the same change.

**Policy: permissive licenses only.** Toby may only depend on modules under
permissive licenses (MIT, BSD-2/3-Clause, ISC, Apache-2.0, MPL-2.0, and similar).
We must **never** pull in a copyleft license — GPL, AGPL, LGPL, SSPL, or any
license with comparable reciprocal/source-disclosure obligations — whether as a
direct or an indirect dependency. Before adding or updating a module, verify its
license (and any new indirect dependencies it drags in) and reject anything that
is not clearly permissive. See [AGENTS.md](../AGENTS.md) for the workflow.

To re-derive a license, read the `LICENSE`/`COPYING` file in the module's cache
directory (`$(go env GOMODCACHE)/<escaped-module-path>@<version>/`).

## Direct dependencies

| Module | Version | License |
| --- | --- | --- |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/moby/moby/api | v1.54.1 | Apache-2.0 |
| github.com/moby/moby/client | v0.4.0 | Apache-2.0 |
| github.com/modelcontextprotocol/go-sdk | v1.2.0 | MIT |
| github.com/pelletier/go-toml/v2 | v2.3.1 | MIT |
| github.com/spf13/cobra | v1.10.2 | Apache-2.0 |
| github.com/testcontainers/testcontainers-go | v0.42.0 | MIT |
| go.uber.org/dig | v1.19.0 | MIT |
| go.uber.org/fx | v1.24.0 | MIT |
| golang.org/x/term | v0.43.0 | BSD-3-Clause |
| google.golang.org/grpc | v1.80.0 | Apache-2.0 |
| google.golang.org/protobuf | v1.36.11 | BSD-3-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | MIT and Apache-2.0 |

## Indirect dependencies

| Module | Version | License |
| --- | --- | --- |
| dario.cat/mergo | v1.0.2 | BSD-3-Clause |
| github.com/Azure/go-ansiterm | v0.0.0-20250102033503-faa5f7b0171c | MIT |
| github.com/Microsoft/go-winio | v0.6.2 | MIT |
| github.com/cenkalti/backoff/v4 | v4.3.0 | MIT |
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT |
| github.com/containerd/errdefs | v1.0.0 | Apache-2.0 |
| github.com/containerd/errdefs/pkg | v0.3.0 | Apache-2.0 |
| github.com/containerd/log | v0.1.0 | Apache-2.0 |
| github.com/containerd/platforms | v0.2.1 | Apache-2.0 |
| github.com/cpuguy83/dockercfg | v0.3.2 | MIT |
| github.com/davecgh/go-spew | v1.1.1 | ISC |
| github.com/distribution/reference | v0.6.0 | Apache-2.0 |
| github.com/docker/go-connections | v0.6.0 | Apache-2.0 |
| github.com/docker/go-units | v0.5.0 | Apache-2.0 |
| github.com/ebitengine/purego | v0.10.0 | Apache-2.0 |
| github.com/felixge/httpsnoop | v1.0.4 | MIT |
| github.com/go-logr/logr | v1.4.3 | Apache-2.0 |
| github.com/go-logr/stdr | v1.2.2 | Apache-2.0 |
| github.com/go-ole/go-ole | v1.2.6 | MIT |
| github.com/google/jsonschema-go | v0.3.0 | MIT |
| github.com/inconshreveable/mousetrap | v1.1.0 | Apache-2.0 |
| github.com/klauspost/compress | v1.18.5 | Apache-2.0 |
| github.com/lufia/plan9stats | v0.0.0-20211012122336-39d0f177ccd0 | BSD-3-Clause |
| github.com/magiconair/properties | v1.8.10 | BSD-2-Clause |
| github.com/moby/docker-image-spec | v1.3.1 | Apache-2.0 |
| github.com/moby/go-archive | v0.2.0 | Apache-2.0 |
| github.com/moby/patternmatcher | v0.6.1 | Apache-2.0 |
| github.com/moby/sys/sequential | v0.6.0 | Apache-2.0 |
| github.com/moby/sys/user | v0.4.0 | Apache-2.0 |
| github.com/moby/sys/userns | v0.1.0 | Apache-2.0 |
| github.com/moby/term | v0.5.2 | Apache-2.0 |
| github.com/opencontainers/go-digest | v1.0.0 | Apache-2.0 |
| github.com/opencontainers/image-spec | v1.1.1 | Apache-2.0 |
| github.com/pmezard/go-difflib | v1.0.0 | BSD-2-Clause |
| github.com/power-devops/perfstat | v0.0.0-20240221224432-82ca36839d55 | MIT |
| github.com/shirou/gopsutil/v4 | v4.26.3 | BSD-3-Clause |
| github.com/sirupsen/logrus | v1.9.4 | MIT |
| github.com/spf13/pflag | v1.0.9 | BSD-3-Clause |
| github.com/stretchr/testify | v1.11.1 | MIT |
| github.com/tklauser/go-sysconf | v0.3.16 | BSD-3-Clause |
| github.com/tklauser/numcpus | v0.11.0 | Apache-2.0 |
| github.com/yosida95/uritemplate/v3 | v3.0.2 | BSD-3-Clause |
| github.com/yusufpapurcu/wmi | v1.2.4 | MIT |
| go.opentelemetry.io/auto/sdk | v1.2.1 | Apache-2.0 |
| go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp | v0.60.0 | Apache-2.0 |
| go.opentelemetry.io/otel | v1.41.0 | Apache-2.0 |
| go.opentelemetry.io/otel/metric | v1.41.0 | Apache-2.0 |
| go.opentelemetry.io/otel/trace | v1.41.0 | Apache-2.0 |
| go.uber.org/multierr | v1.10.0 | MIT |
| go.uber.org/zap | v1.26.0 | MIT |
| golang.org/x/crypto | v0.48.0 | BSD-3-Clause |
| golang.org/x/net | v0.49.0 | BSD-3-Clause |
| golang.org/x/oauth2 | v0.34.0 | BSD-3-Clause |
| golang.org/x/sys | v0.44.0 | BSD-3-Clause |
| golang.org/x/text | v0.34.0 | BSD-3-Clause |
| google.golang.org/genproto/googleapis/rpc | v0.0.0-20260120221211-b8f7ae30c516 | Apache-2.0 |
