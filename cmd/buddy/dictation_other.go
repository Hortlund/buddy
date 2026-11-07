//go:build !darwin

package main

const supportsDictation = false

func startSystemDictation() bool { return false }
