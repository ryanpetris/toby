MUSL_TARGET ?= x86_64-unknown-linux-musl

.PHONY: all check fmt fmt-check lint test deny build static static-test

all: check

check: fmt-check lint test deny

fmt:
	cargo fmt --all

fmt-check:
	cargo fmt --all --check

lint:
	cargo clippy --workspace --all-targets -- -D warnings

test:
	cargo test --workspace

deny:
	cargo deny check

build:
	cargo build --workspace

# Fully static release binary; needs a musl C compiler (x86_64-linux-musl-gcc).
static:
	cargo build --release --locked --target $(MUSL_TARGET) -p toby

# Runs the binary's tests against the static build.
static-test:
	cargo test --release --locked --target $(MUSL_TARGET) -p toby
