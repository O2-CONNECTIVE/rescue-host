//go:build !windows

package bootstrapauth

func platformKernel() kernel { return nil }
