//go:build !windows

package serviceready

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"

func platformKernel(*bootstrapauth.Factory) kernel { return nil }
