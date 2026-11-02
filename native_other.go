//go:build !darwin || !cgo

package main

import "errors"

func loadSpotlightBridge() (spotlightBridge, error) {
	return spotlightBridge{}, errors.New("Search Privacy requires macOS and a build with cgo enabled")
}
func nativeBackupWrite(path string, excluded bool) error {
	return errors.New("Backup resource properties require macOS and a build with cgo enabled")
}
