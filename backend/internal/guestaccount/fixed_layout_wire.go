package guestaccount

import (
	"encoding/hex"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

type fixedLayoutArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type fixedLayoutFile struct {
	Path         string `json:"path"`
	Role         string `json:"role"`
	Directory    bool   `json:"directory"`
	Volume       string `json:"volume"`
	FileID       string `json:"fileID"`
	OwnerSID     string `json:"ownerSID"`
	SecuritySDDL string `json:"securitySDDL"`
}
type fixedLayoutPolicy struct {
	Groups      []string `json:"groups"`
	Privileges  []string `json:"privileges"`
	LogonRights []string `json:"logonRights"`
}
type fixedLayoutWire struct {
	Protocol       string `json:"protocol"`
	Installation   string `json:"installation"`
	MachineSID     string `json:"machineSID"`
	ServiceUserSID string `json:"serviceUserSID"`
	ServiceSID     string `json:"serviceSID"`
	OperatorSID    string `json:"operatorSID"`
	ServiceName    string `json:"serviceName"`
	JournalRoot    string `json:"journalRoot"`
	Powershell     struct {
		Image    fixedLayoutArtifact `json:"image"`
		Manifest fixedLayoutArtifact `json:"manifest"`
		Assembly fixedLayoutArtifact `json:"assembly"`
	} `json:"powershell"`
	Files  []fixedLayoutFile `json:"files"`
	Policy fixedLayoutPolicy `json:"policy"`
}

func fixedLayoutConvert(w fixedLayoutWire) (*FixedLayout, error) {
	f := &FixedLayout{machine: w.MachineSID, user: w.ServiceUserSID, operator: w.OperatorSID, policy: limitedPolicy{known: true, groups: append([]string{}, w.Policy.Groups...), privileges: append([]string{}, w.Policy.Privileges...), logonRights: append([]string{}, w.Policy.LogonRights...)}}
	c := stagingProfile{serviceName: w.ServiceName, serviceSID: w.ServiceSID, journalRoot: w.JournalRoot}
	if !fixedLayoutHex(w.Installation, c.installation[:]) {
		return nil, ErrUnconfigured
	}
	var e error
	c.native.image, e = fixedLayoutImage(w.Powershell.Image)
	if e != nil {
		return nil, e
	}
	c.native.manifest, e = fixedLayoutImage(w.Powershell.Manifest)
	if e != nil {
		return nil, e
	}
	c.native.assembly, e = fixedLayoutImage(w.Powershell.Assembly)
	if e != nil {
		return nil, e
	}
	for _, v := range w.Files {
		n, e := strconv.ParseUint(v.Volume, 10, 64)
		if e != nil || strconv.FormatUint(n, 10) != v.Volume {
			return nil, ErrUnconfigured
		}
		item := stagingFile{path: v.Path, directory: v.Directory, identity: journalIdentity{volume: n, owner: v.OwnerSID, security: v.SecuritySDDL}}
		if !fixedLayoutHex(v.FileID, item.identity.fileID[:]) {
			return nil, ErrUnconfigured
		}
		switch v.Role {
		case "configuration":
			item.role = roleConfiguration
		case "journal":
			item.role = roleJournal
		case "hostKey":
			item.role = roleHostKey
		case "artifact":
			item.role = roleArtifact
		case "ancestor":
			item.role = roleAncestor
		default:
			return nil, ErrUnconfigured
		}
		c.files = append(c.files, item)
	}
	f.profile = c
	return f, nil
}
func fixedLayoutHex(text string, out []byte) bool {
	if len(text) != 2*len(out) {
		return false
	}
	n, e := hex.Decode(out, []byte(text))
	return e == nil && n == len(out) && hex.EncodeToString(out) == text
}
func fixedLayoutImage(v fixedLayoutArtifact) (childjob.Artifact, error) {
	a := childjob.Artifact{Path: v.Path}
	if !fixedLayoutHex(v.SHA256, a.SHA256[:]) {
		return childjob.Artifact{}, ErrUnconfigured
	}
	return a, nil
}
