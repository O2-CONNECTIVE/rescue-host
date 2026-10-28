package bootstrap

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

type wireGrant struct {
	Generation            string               `json:"generation"`
	Identity              control.WireIdentity `json:"identity"`
	KeySetDigest          string               `json:"keySetDigest"`
	GuestBoundary         string               `json:"guestBoundary"`
	HardDeadline          string               `json:"hardDeadline"`
	BackendLeaseCeilingNs string               `json:"backendLeaseCeilingNs"`
}
type wireConfig struct {
	OperationMode     string   `json:"operationMode"`
	AllowedKeys       []string `json:"allowedKeys"`
	MaximumDurationNs string   `json:"maximumDurationNs"`
	LeaseDurationNs   string   `json:"leaseDurationNs"`
	MaxChannels       string   `json:"maxChannels"`
	MaxHandshakes     string   `json:"maxHandshakes"`
	MaxTransports     string   `json:"maxTransports"`
}
type wireStdio struct {
	Frames           string `json:"frames"`
	CleanupTimeoutNs string `json:"cleanupTimeoutNs"`
}
type wireRecord struct {
	Protocol            string          `json:"protocol"`
	IssuerInstanceEpoch string          `json:"issuerInstanceEpoch"`
	Grant               wireGrant       `json:"grant"`
	Binding             control.Binding `json:"binding"`
	PolicyRef           string          `json:"policyRef"`
	Backend             wireConfig      `json:"backend"`
	HostKeyRef          string          `json:"hostKeyRef"`
	Stdio               wireStdio       `json:"stdio"`
}

// EncodeRecord は一つの JSON object と LF を返す。I/O は行わない。
func EncodeRecord(message Message) ([]byte, error) {
	if !validMessage(message) {
		return nil, ErrRecord
	}
	g, c := message.Grant, message.Config
	w := wireRecord{Protocol: Protocol, IssuerInstanceEpoch: message.IssuerInstanceEpoch, Grant: wireGrant{Generation: strconv.FormatUint(g.Binding.Generation, 10), Identity: message.Wire.Identity, KeySetDigest: hex.EncodeToString(g.KeySetDigest[:]), GuestBoundary: "separated_non_admin_guest", HardDeadline: g.HardDeadline.UTC().Format(time.RFC3339Nano), BackendLeaseCeilingNs: strconv.FormatInt(int64(g.BackendLeaseCeiling), 10)}, Binding: message.Wire.Binding, PolicyRef: message.Wire.PolicyRef, HostKeyRef: message.HostKeyRef, Backend: wireConfig{OperationMode: c.OperationMode, AllowedKeys: c.AllowedKeys, MaximumDurationNs: strconv.FormatInt(int64(c.MaximumDuration), 10), LeaseDurationNs: strconv.FormatInt(int64(c.LeaseDuration), 10), MaxChannels: strconv.Itoa(c.MaxChannels), MaxHandshakes: strconv.Itoa(c.MaxHandshakes), MaxTransports: strconv.Itoa(c.MaxTransports)}, Stdio: wireStdio{Frames: strconv.Itoa(message.Limits.Frames), CleanupTimeoutNs: strconv.FormatInt(int64(message.Limits.CleanupTimeout), 10)}}
	record, err := json.Marshal(w)
	if err != nil || len(record)+1 > RecordBytes {
		return nil, ErrRecord
	}
	return append(record, '\n'), nil
}

// DecodeRecord は slice 全体の構造だけを検査し、実 pipe EOF は観測しない。
// Expected は呼出し前から独立に既知の全値を要求する。認可は返さない。
// native 受信では認証後の package 内経路を別途使い、payload から Expected を作らない。
func DecodeRecord(record []byte, expected Expected, limits ReceiverLimits) (Message, error) {
	if limits.MaxFrames < 1 || int64(limits.MaxFrames) > 2147483647 || limits.MaxCleanupTimeout <= 0 {
		return Message{}, ErrLimits
	}
	if !strictRecord(record) {
		return Message{}, ErrRecord
	}
	var w wireRecord
	if json.Unmarshal(record, &w) != nil || w.Protocol != Protocol || w.Grant.GuestBoundary != "separated_non_admin_guest" {
		return Message{}, ErrRecord
	}
	generation, ok := decimal(w.Grant.Generation, 64, true)
	if !ok || !isDigest(w.Grant.KeySetDigest) {
		return Message{}, ErrRecord
	}
	digest, _ := hex.DecodeString(w.Grant.KeySetDigest)
	deadline, err := time.Parse(time.RFC3339Nano, w.Grant.HardDeadline)
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != w.Grant.HardDeadline {
		return Message{}, ErrRecord
	}
	values := []string{w.Grant.BackendLeaseCeilingNs, w.Backend.MaximumDurationNs, w.Backend.LeaseDurationNs, w.Backend.MaxChannels, w.Backend.MaxHandshakes, w.Backend.MaxTransports, w.Stdio.Frames, w.Stdio.CleanupTimeoutNs}
	var parsed [8]uint64
	for i, value := range values {
		n, ok := decimal(value, 63, true)
		if !ok {
			return Message{}, ErrRecord
		}
		parsed[i] = n
	}
	// int への変換前に OS の語長に依存しない上限を照合する。
	if parsed[3] > 32 || parsed[4] > 64 || parsed[5] > 64 || parsed[6] > 2147483647 {
		return Message{}, ErrRecord
	}
	id := backend.Identity{LaunchID: w.Grant.Identity.LaunchID, Epoch: w.Grant.Identity.Epoch, PolicyDigest: w.Grant.Identity.PolicyDigest}
	g := supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: generation, Identity: id}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: deadline, BackendLeaseCeiling: time.Duration(parsed[0])}
	copy(g.KeySetDigest[:], digest)
	m := Message{IssuerInstanceEpoch: w.IssuerInstanceEpoch, Grant: g, Wire: control.WireContext{Generation: w.Grant.Generation, Identity: w.Grant.Identity, Binding: w.Binding, PolicyRef: w.PolicyRef}, HostKeyRef: w.HostKeyRef, Config: Config{OperationMode: w.Backend.OperationMode, AllowedKeys: w.Backend.AllowedKeys, MaximumDuration: time.Duration(parsed[1]), LeaseDuration: time.Duration(parsed[2]), MaxChannels: int(parsed[3]), MaxHandshakes: int(parsed[4]), MaxTransports: int(parsed[5])}, Limits: stdio.Limits{FrameBytes: RecordBytes, Frames: int(parsed[6]), CleanupTimeout: time.Duration(parsed[7])}}
	if !validMessage(m) {
		return Message{}, ErrRecord
	}
	if m.Limits.Frames > limits.MaxFrames || m.Limits.CleanupTimeout > limits.MaxCleanupTimeout {
		return Message{}, ErrLimits
	}
	if !matches(m, expected) {
		return Message{}, ErrExpected
	}
	return m, nil
}
