// Package buildinfo identifies the same release across desktop, server and workers.
package buildinfo

// Release builds set these values using the Go linker.
var Version = "0.1.0-dev"
var Commit = "unknown"
var Date = "unknown"
