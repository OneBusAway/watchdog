package db

// NewProvider exposes newProvider to the external db_test package, which
// cannot reach unexported names. This file is compiled only for tests.
var NewProvider = newProvider
