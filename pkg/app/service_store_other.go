//go:build !windows

package app

func serviceLocalDataDir() (string, error)        { return "", errServicePlatform }
func protectServiceData([]byte) ([]byte, error)   { return nil, errServicePlatform }
func unprotectServiceData([]byte) ([]byte, error) { return nil, errServicePlatform }
