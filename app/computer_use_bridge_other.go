//go:build !darwin

package main

func startComputerUseHost() (interface{ Close() }, error) { return nil, nil }
