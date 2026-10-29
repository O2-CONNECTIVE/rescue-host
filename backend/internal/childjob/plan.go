package childjob

import (
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
)

type launchPlan struct {
	application, commandLine, directory string
	environment                         [2]uint16
	handles                             [3]uintptr
	flags, startFlags                   uint32
	bootstrap                           uintptr
}

func (p launchPlan) inheritedHandles() []uintptr {
	values := append([]uintptr(nil), p.handles[:]...)
	if p.bootstrap != 0 {
		values = append(values, p.bootstrap)
	}
	return values
}

func bootstrapArgs(spec Spec) ([]string, error) {
	b := spec.Bootstrap
	if b == nil {
		return []string{"--supervised-internal"}, nil
	}
	if !ordinaryHandle(b.ReadHandle) || b.ReadHandle == spec.PrimaryToken || b.Values.Wire.Binding.ArtifactSHA256 != hex.EncodeToString(spec.Artifact.SHA256[:]) {
		return nil, ErrConfig
	}
	args, err := bootstrap.FormatLaunchArgs(b.Values, b.ReadHandle, [3]uintptr{spec.Handles.Stdin, spec.Handles.Stdout, spec.Handles.Stderr})
	if err != nil {
		return nil, ErrConfig
	}
	return args, nil
}

// ローカルdriveの正規絶対pathだけを扱う。UNC/device/search pathを採らない。
// pathの真正性や祖先directoryの保護は、この構文検査では証明しない。
func localImagePath(path string) bool {
	return localFilePath(path) && strings.HasSuffix(strings.ToLower(path), ".exe")
}

func localFilePath(path string) bool {
	if len(path) < 4 || len(path) > maxCommandUnits*4 || !utf8.ValidString(path) || utf16Units(path) > maxCommandUnits || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` {
		return false
	}
	for _, segment := range strings.Split(path[3:], `\`) {
		if segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") || strings.ContainsAny(segment, `<>:"/|?*`) {
			return false
		}
		for _, r := range segment {
			if r < 32 {
				return false
			}
		}
	}
	return true
}

func makePlan(spec Spec) (launchPlan, error) {
	if !configured(spec) || !localImagePath(spec.Artifact.Path) {
		return launchPlan{}, ErrConfig
	}
	args, err := launchArgs(spec)
	if err != nil {
		return launchPlan{}, ErrConfig
	}
	if spec.Command != nil {
		args = append([]string(nil), args...)
		for i, arg := range args {
			args[i] = escapeWindowsArg(arg)
		}
	}
	separator := strings.LastIndex(spec.Artifact.Path, `\`)
	directory := spec.Artifact.Path[:separator]
	if separator == 2 {
		directory += `\`
	}
	var read uintptr
	if spec.Bootstrap != nil {
		read = spec.Bootstrap.ReadHandle
	}
	return launchPlan{
		application: spec.Artifact.Path,
		commandLine: `"` + spec.Artifact.Path + `" ` + strings.Join(args, " "),
		directory:   directory,
		handles:     [3]uintptr{spec.Handles.Stdin, spec.Handles.Stdout, spec.Handles.Stderr},
		// CREATE_SUSPENDED | CREATE_UNICODE_ENVIRONMENT | EXTENDED_STARTUPINFO_PRESENT | CREATE_NO_WINDOW。
		flags: 0x08080404, startFlags: 0x100, // STARTF_USESTDHANDLES。
		bootstrap: read,
	}, nil
}
