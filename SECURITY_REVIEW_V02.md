# Rescue Host v0.2 independent security review

Review date: 2026-10-26 UTC. This is a **new review produced during this task**, not the missing prior review/HANDOFF supplied by the user. Scope: local Python source and offline fake-service tests; exact upstream Upterm v0.39.0 source. No production PC, public relay, real credentials, firewall/sshd changes, or remote exposure were used.

**Release verdict: blocked for remote use.** The safe development outcome is a non-configurable new-session gate, with legacy-session cleanup retained. This is not delivery of a functioning strictly restricted remote-access product.

## Findings in the supplied baseline

### 1. SSH mode did not enforce its advertised destination or SSH-only boundary — critical

`src/rescue_core.py:upterm_cmd_start` supplied `--allow-local-tcp-forwarding` and launched writable PowerShell. `build_mac_forward_command` only suggested a client-side `-L` destination. An authorized guest could choose a different host/port or open the shared terminal rather than authenticate to the inner Windows sshd. A successful loopback TCP connection in `check_local_sshd` establishes neither SSH service identity nor its authentication policy.

Upterm's actual destination callback logs the requested destination but returns true solely from the global forwarding boolean: [v0.39.0 server.go, lines 349–364](https://github.com/owenthereal/upterm/blob/v0.39.0/host/internal/server.go#L349-L364). Its [host CLI documentation](https://github.com/owenthereal/upterm/blob/v0.39.0/docs/upterm_host.md) describes broad host-reachable forwarding. `--read-only` is incompatible with forwarding, not a forwarding-only mode. `--no-sftp` disables that subsystem, not file access through a writable shell.

Adding OpenSSH `permitopen`, `no-pty`, or forced-command options to Upterm's authorized-key file cannot supply the missing policy: [authorizedkeys.go](https://github.com/owenthereal/upterm/blob/v0.39.0/host/authorizedkeys.go) ignores the parsed options and retains public keys only.

### 2. Inherited config could broaden authorization or weaken controls — high

`run_upterm` removed `UPTERM_*` environment variables but still loaded the user's Upterm configuration. Omitted settings such as `authorized-user`, legacy provider-specific authorization, `allow-local-tcp-forwarding`, `skip-host-key-check`, and `force-command` could change the effective policy. The selected authorized-key file is not an exclusive boundary when additional authorized-user sources are configured.

Upstream [config.go](https://github.com/owenthereal/upterm/blob/v0.39.0/cmd/upterm/command/config.go) and [root.go](https://github.com/owenthereal/upterm/blob/v0.39.0/cmd/upterm/command/root.go) establish CLI > environment > config > defaults. [utils.go, lines 121–150](https://github.com/owenthereal/upterm/blob/v0.39.0/utils/utils.go#L121-L150) confirms an explicit `XDG_CONFIG_HOME` selects `<root>/upterm/config.yaml`, including Windows. Isolate that configuration and explicitly set security-critical booleans. Do not change runtime/state roots when locating legacy sessions.

### 3. Deadline/watchdog was best-effort, not guaranteed revocation — high

`src/rescue_watchdog.py` originally treated successful `Popen` as readiness, used wall-clock time alone, refused sufficiently overdue deadlines, and stopped retrying after two failures. `RescueApp._tick` tried once. A crash, clock rollback, startup/import failure, or same-user termination could leave a detached daemon accessible beyond the intended duration. Deleting the staged key file does not revoke keys already read into the daemon.

Important distinction: genuine upstream `session stop` is **not merely an acknowledgement**. [session.go, lines 672–787](https://github.com/owenthereal/upterm/blob/v0.39.0/cmd/upterm/command/session.go#L672-L787) sends a launch-bound request and waits for its name/launch ownership to be released; uncertainty is an error. Its wait budget is 30 seconds, with 10-second query budgets, so the wrapper's former 15-second subprocess deadline could interrupt legitimate completion. Exit 4 means no such session in the selected local state context, not proof about every process on the PC.

The trusted guest runs as the same account as the watchdog and can interfere with it or create persistent effects. A Python process cannot be advertised as tamper-resistant expiration enforcement. `--join-timeout` is also not a lease: a terminal/SFTP guest permanently disarms it, and forwarding alone does not satisfy it.

### 4. Host version and relay trust are separate requirements — high

The local v0.39 minimum is not evidence that a custom relay is patched. The [official GHSA-p5vj-9fhj-87q7 advisory](https://github.com/owenthereal/upterm/security/advisories/GHSA-p5vj-9fhj-87q7) requires both host and every serving relay node to be patched (fix: v0.33.0). A patched host alone does not fix an unpatched relay.

The host trusts the verified relay to attest guest identity: [relayauthority.go](https://github.com/owenthereal/upterm/blob/v0.39.0/host/internal/relayauthority.go) binds guest-certificate authority to the accepted relay host key; [server.go](https://github.com/owenthereal/upterm/blob/v0.39.0/host/internal/server.go#L875-L899) compares the resulting guest key against the allowlist. Relay key pinning is necessary but not a relay-patch or honest-relay attestation.

### 5. Recovery identity and evidence need conservative handling — medium

The baseline silently treated malformed recovery state as absent, resolved executables again from mutable paths, and applied asynchronous info responses to whichever session was currently displayed. Stop failures must preserve state and key evidence. Recovery must not label uncertainty as successful termination. Unknown states, malformed JSON, stale session identities, and ambiguous process lookup need explicit treatment.

## Required regression coverage

- Both GUI and worker paths reject new sessions before subprocess invocation; no configuration/environment escape hatch.
- SSH command construction and forward-command copying reject the unsupported restricted-forwarding mode.
- Malformed, empty, certificate/option-bearing, truncated, or trailing-data public keys fail closed.
- Inherited Upterm env/config cannot add keys, enable forwarding, or disable host-key checking; runtime/state lookup roots remain unchanged for recovery.
- Stop success/exit 4 with contradictory active status, malformed status, lookup error, and nonzero stop retain recovery evidence; confirmed ended status permits cleanup.
- Watchdog readiness is acknowledged, non-finite deadlines are rejected, overdue sessions are not ignored, and wall-clock rollback does not extend a running watchdog.
- Stale asynchronous results cannot remove another session's recovery record.

## Conditions before removing the release gate

1. Supply and reconcile the missing original HANDOFF/review; document intended principals, relay trust, inner sshd policy, and the exact meaning of revocation.
2. Implement a reviewed host-side channel policy that permits only the literal intended TCP destination/port and rejects shell, exec, PTY, SFTP, agent, reverse, and other forwarding channels as appropriate. Client command text is never the security boundary. Do not silently substitute terminal mode.
3. Enforce expiry in an authority the remote guest cannot disable. Establish Windows process/job/service containment, handle ownership, child escape policy, and failure behavior before any exposure.
4. In an explicitly authorized isolated Windows environment, verify unauthorized keys and forged certificates fail; allowed keys succeed only on allowed channels; other loopback/LAN/IPv6/DNS destinations fail; live streams and reconnects fail after stop/expiry; GUI/watchdog crashes, sleep/resume, clock changes, network reconnects, and recovery preserve the boundary.
5. Verify the actual Windows binary/package, supported CLI schema, config isolation, relay patch status, pinned trust material, and licensing. A checksum downloaded beside a release archive is an integrity check against that same source, not an independent publisher signature.

## Verification status

Independent final local run on 2026-10-26 at approximately 05:39 UTC:

```text
python -m unittest discover -s tests -q
Ran 42 tests in 0.260s — OK
python -m compileall -q src tests
Exit 0
```

Reviewed the final source and test changes, not only the test summary:

- GUI and worker entrypoints call the unconditional release gate before port probes, executable resolution, staging, or process launch. SSH-mode argument construction and both clipboard sharing paths are disabled. Terminal command construction remains internal scaffold, not an available start path.
- `run_upterm` writes an empty private application config and sets `XDG_CONFIG_HOME`; it removes inherited `UPTERM_*` values while retaining existing runtime/state roots for legacy discovery. Same-user file tampering is not excluded by this design and is not claimed to be.
- Strict public-key wire parsing, fail-closed recovery parsing, unknown-status rejection, command-data minimization, and stale-poll/stop-event name checks are present. Corrupt recovery state raises an error; `main` displays a blocking error and preserves the file, rather than opening as if no session existed. No graphical Windows run was performed.
- Stop uses a 65-second CLI budget, then requires explicit ended status. Exit 4 from info is now **uncertain**, preserving evidence because legacy executable/state context was not bound. Failed stop, malformed output, or lookup error does not call recovery cleanup.
- Watchdog startup verifies a per-session receipt and cleans up its own newly spawned process on failed acknowledgement. Deadline checking combines wall and monotonic clocks; overdue sessions are attempted immediately. The five-attempt retry budget and one-shot GUI expiry attempt remain best-effort recovery, never an enforcement guarantee.
- README, new handoff, and backend contract accurately label the candidate as blocked for new remote use and distinguish fake lifecycle tests from real security evidence. Build script was inspected, not executed.

### Remaining limits of this candidate

Legacy records do not bind an executable hash, original `XDG_STATE_HOME`/`XDG_RUNTIME_DIR`, and immutable launch identity. An absolute executable path alone is mutable; no retroactive authenticity can be inferred. Even an explicit ended record is evidence from the selected CLI/context, not independent proof that every historical transport or descendant process is gone. A selected or replaced malicious executable could lie. Before any new session is enabled, record and verify binary and launch identity plus lookup context, and enforce containment outside guest control. The current gate prevents creating new sessions with these incomplete guarantees.

The unit suite includes negative key/config/state/stop cases, stale polls and stop events, dangling state symlinks, non-finite/overdue/rollback deadlines, and failed watchdog receipt cleanup. It does not exhaustively fault-inject every GUI event, filesystem race, process creation failure, sleep/resume path, or Windows ACL behavior. Corrupt-state startup handling was source-reviewed; no real GUI was exercised.

**Accepted only as a fail-closed development/recovery candidate, not approved for production remote access.** Fake CLI tests do not establish cryptographic SSH authentication, live TCP destination filtering, Windows process containment, or guaranteed revocation. The missing original HANDOFF/review and all release conditions above remain open.
