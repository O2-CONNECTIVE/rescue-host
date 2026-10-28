//go:build !windows

package operatorauth

func nativeReportKernel() (reportKernel, error) { return nil, ErrUnconfigured }
