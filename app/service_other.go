//go:build !darwin

package main

// On other platforms the app runs the engine as a child process (or uses an
// engine already started with `umcode service install`).

func serviceStatus() serviceState     { return serviceUnsupported }
func serviceCanUseSMAppService() bool { return false }
func registerService() error          { return errServiceUnsupported }
func unregisterService() error        { return errServiceUnsupported }
func restartService() error           { return errServiceUnsupported }
func openLoginItemsSettings()         {}
