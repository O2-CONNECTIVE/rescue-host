package childjob

import "strings"

type launchPlan struct {
	application, commandLine, directory string
	environment                         [2]uint16
	handles                             [3]uintptr
	flags, startFlags                   uint32
}

// ローカルdriveの正規絶対pathだけを扱う。UNC/device/search pathを採らない。
// pathの真正性や祖先directoryの保護は、この構文検査では証明しない。
func localImagePath(path string) bool {
	if len(path) < 7 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` || !strings.HasSuffix(strings.ToLower(path), ".exe") {
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
	separator := strings.LastIndex(spec.Artifact.Path, `\`)
	directory := spec.Artifact.Path[:separator]
	if separator == 2 {
		directory += `\`
	}
	return launchPlan{
		application: spec.Artifact.Path,
		commandLine: `"` + spec.Artifact.Path + `" --supervised-internal`,
		directory:   directory,
		handles:     [3]uintptr{spec.Handles.Stdin, spec.Handles.Stdout, spec.Handles.Stderr},
		// CREATE_SUSPENDED | CREATE_UNICODE_ENVIRONMENT | EXTENDED_STARTUPINFO_PRESENT | CREATE_NO_WINDOW。
		flags: 0x08080404, startFlags: 0x100, // STARTF_USESTDHANDLES。
	}, nil
}
