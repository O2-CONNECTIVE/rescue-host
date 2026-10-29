package childjob

import (
	"strings"
	"unicode/utf8"
)

const (
	maxFixedArgs          = 64
	maxPinnedDependencies = 32
	// CreateProcessの32767 UTF-16文字上限から末尾NULを除きます。
	maxCommandUnits = 32766
)

// FixedCommand はGo側の信頼済profileが固定したargvと依存fileのsnapshotです。
// 認証proofではなく、UI/argv/環境/保存stateをこのconstructorへ渡してはなりません。
// fieldを公開せず、constructorとStartの両方でsliceの所有を分離します。
type FixedCommand struct {
	args         []string
	dependencies []Artifact
	configured   bool
}

func NewFixedCommand(args []string, pinnedDependencies []Artifact) (*FixedCommand, error) {
	command := &FixedCommand{args: append([]string(nil), args...), dependencies: append([]Artifact(nil), pinnedDependencies...), configured: true}
	if !validCommand(command) {
		return nil, ErrConfig
	}
	return command, nil
}

func utf16Units(value string) int {
	n := 0
	for _, r := range value {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func artifactClashes(a, b Artifact) bool {
	return strings.EqualFold(a.Path, b.Path)
}

func validCommand(command *FixedCommand) bool {
	if command == nil || !command.configured || len(command.args) == 0 || len(command.args) > maxFixedArgs || len(command.dependencies) > maxPinnedDependencies {
		return false
	}
	units := 0
	for _, arg := range command.args {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) || len(arg) > maxCommandUnits*4 {
			return false
		}
		units += utf16Units(escapeWindowsArg(arg)) + 1
		if units > maxCommandUnits {
			return false
		}
	}
	for i, dependency := range command.dependencies {
		if !localFilePath(dependency.Path) || dependency.SHA256 == ([32]byte{}) {
			return false
		}
		for _, other := range command.dependencies[:i] {
			if artifactClashes(dependency, other) {
				return false
			}
		}
	}
	return true
}

func copyCommand(command *FixedCommand) *FixedCommand {
	if command == nil {
		return nil
	}
	return &FixedCommand{args: append([]string(nil), command.args...), dependencies: append([]Artifact(nil), command.dependencies...), configured: command.configured}
}

func launchArgs(spec Spec) ([]string, error) {
	if spec.Command == nil {
		return bootstrapArgs(spec)
	}
	if spec.Bootstrap != nil || !validCommand(spec.Command) || !localImagePath(spec.Artifact.Path) {
		return nil, ErrConfig
	}
	units := utf16Units(spec.Artifact.Path) + 2
	for _, arg := range spec.Command.args {
		units += 1 + utf16Units(escapeWindowsArg(arg))
	}
	if units > maxCommandUnits {
		return nil, ErrConfig
	}
	for _, dependency := range spec.Command.dependencies {
		if artifactClashes(spec.Artifact, dependency) {
			return nil, ErrConfig
		}
	}
	return spec.Command.args, nil
}

// Windows EscapeArgと同じbackslash/quote規則。shellを介さず1引数を構成します。
func escapeWindowsArg(arg string) string {
	if arg == "" {
		return `""`
	}
	quoted := strings.ContainsAny(arg, " \t")
	var out strings.Builder
	if quoted {
		out.WriteByte('"')
	}
	slashes := 0
	for _, r := range arg {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			out.WriteString(strings.Repeat(`\`, slashes*2+1))
		} else {
			out.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		out.WriteRune(r)
	}
	if quoted {
		out.WriteString(strings.Repeat(`\`, slashes*2))
		out.WriteByte('"')
	} else {
		out.WriteString(strings.Repeat(`\`, slashes))
	}
	return out.String()
}
