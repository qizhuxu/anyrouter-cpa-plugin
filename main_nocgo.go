//go:build !cgo

package main

// This file exists so the package still builds, vets and unit-tests with
// CGO_ENABLED=0: cabi.go (the real entry point with the cgo preamble) is
// implicitly excluded from cgo-free builds, which would otherwise leave the
// main package without a main function. Plugin artifacts are always built with
// cgo enabled.
func main() {}
