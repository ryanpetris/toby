package storage

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/sqlite-schema.sql
var storageSQLiteSchemaFixture string

//go:embed testdata/sqlite-worker.sql
var storageSQLiteWorkerFixture string

//go:embed testdata/sqlite-verify.sql
var storageSQLiteVerifyFixture string
