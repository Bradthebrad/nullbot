//go:build !linux && !darwin && !windows

package llmfit

import (
	"context"
	"errors"
)

func nativeHost(ctx context.Context, d *detector) (hostInfo, error) {
	return hostInfo{}, errors.New("reading system memory is not supported on this OS")
}
