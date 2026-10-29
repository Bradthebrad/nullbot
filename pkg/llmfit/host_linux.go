//go:build linux

package llmfit

import "context"

func nativeHost(ctx context.Context, d *detector) (hostInfo, error) { return linuxHost(ctx, d) }
