package bwrap

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/termination-prefix.sh
var backgroundTerminationPrefixFixture string

//go:embed testdata/termination-suffix.sh
var backgroundTerminationSuffixFixture string

//go:embed testdata/descendant-process.sh
var backgroundDescendantScriptFixture string

//go:embed testdata/sqlite-schema.sql
var bwrapSQLiteSchemaFixture string

//go:embed testdata/sqlite-first-writer.sql
var bwrapSQLiteFirstWriterFixture string

//go:embed testdata/sqlite-second-writer.sql
var bwrapSQLiteSecondWriterFixture string

//go:embed testdata/sqlite-verify.sql
var bwrapSQLiteVerifyFixture string

//go:embed testdata/sqlite-shell-prefix.sh
var bwrapSQLiteShellPrefixFixture string

//go:embed testdata/sqlite-shell-suffix.sh
var bwrapSQLiteShellSuffixFixture string

//go:embed testdata/direct-job-control.sh
var directJobControlScriptFixture string

//go:embed testdata/managed-job-control.sh
var managedJobControlScriptFixture string

//go:embed testdata/background-job-control.sh
var backgroundJobControlScriptFixture string

//go:embed testdata/vertical-first-run.sh
var verticalFirstRunScriptFixture string

//go:embed testdata/vertical-second-run.sh
var verticalSecondRunScriptFixture string

//go:embed testdata/vertical-third-run.sh
var verticalThirdRunScriptFixture string
