//go:build !darwin

package secrets

func platformStore() Store { return nil }
