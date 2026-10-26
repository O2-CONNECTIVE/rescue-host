import dataclasses
import pathlib
import sys
import threading
import unittest
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "src"))
from rescue_policy import OperationPolicy, RESTRICTED_HELPERS
from rescue_lease import OfflineSessionAuthority, SessionIdentity, State, Rejected


class Handle:
    def __init__(self):
        self.closed = False
        self.fail = False
    def close(self):
        if self.fail:
            raise OSError("secret error text")
        self.closed = True


class ModelTests(unittest.TestCase):
    def setUp(self):
        self.now = [1000, 0]
        self.policy = OperationPolicy()
        self.identity = SessionIdentity("launch-1", "boot-1", self.policy.digest)
        self.process, self.transport = Handle(), Handle()
        self.model = OfflineSessionAuthority(self.policy, self.identity, clock=lambda: self.now,
            process=self.process, transport=self.transport, max_duration=60, lease_duration=10, max_channels=1)
    def start(self):
        self.model.mark_ready(self.identity)
        self.model.activate(self.identity)
    def advance(self, seconds):
        self.now[:] = [v + seconds for v in self.now]
    def open(self, **kw):
        return self.model.open_channel(self.identity, "direct-tcpip", "127.0.0.1", 22, dial=kw.get("dial", Handle))
    def test_ready_is_required(self):
        with self.assertRaises(Rejected): self.open()
        with self.assertRaises(Rejected): self.model.activate(self.identity)
        self.start()
        with self.assertRaises(Rejected): self.model.mark_ready(self.identity)
    def test_identity_namespaces_and_policy(self):
        for field, value in (("launch_id", "other"), ("run_epoch", "other"), ("policy_digest", "0" * 64)):
            identity = dataclasses.replace(self.identity, **{field: value})
            with self.assertRaises(Rejected): self.model.mark_ready(identity)
        with self.assertRaises(ValueError): dataclasses.replace(self.identity, schema_version=2)
        with self.assertRaises(ValueError): OfflineSessionAuthority(OperationPolicy("full"), self.identity,
            clock=lambda: self.now, process=Handle(), transport=Handle())
    def test_policy_immutable_and_exact(self):
        with self.assertRaises(dataclasses.FrozenInstanceError): self.policy.operation_mode = "full"
        for mode in ("restricted", "full"):
            p = OperationPolicy(mode)
            self.assertFalse(p.allows_admin)
            self.assertEqual(p.allows_inner_shell, mode == "full")
            self.assertTrue(p.permits_channel("direct-tcpip", "127.0.0.1", 22))
            for host in ("localhost", "127.1", "127.0.0.1 ", "::1", "::ffff:127.0.0.1", "10.0.0.1", "8.8.8.8", "", "127.0.0.1\x00"):
                self.assertFalse(p.permits_channel("direct-tcpip", host, 22))
            for port in (True, 22.0, "22", 0, 23):
                self.assertFalse(p.permits_channel("direct-tcpip", "127.0.0.1", port))
            for kind in ("session", "shell", "exec", "pty-req", "subsystem", "sftp", "tcpip-forward", "forwarded-tcpip", "auth-agent@openssh.com", "x11", "direct-streamlocal@openssh.com", "unknown", ""):
                self.assertFalse(p.permits_channel(kind, "127.0.0.1", 22))
            self.assertTrue(p.permits_helper(RESTRICTED_HELPERS[0]))
            for args in (("cmd",), {"command": "cmd"}, [], None):
                self.assertFalse(p.permits_helper(RESTRICTED_HELPERS[0], args))
            self.assertFalse(p.permits_helper("arbitrary-command"))
        self.assertNotEqual(self.policy.digest, OperationPolicy("full").digest)
    def test_reject_never_calls_dial(self):
        self.start()
        def forbidden(): self.fail("must not dial")
        with self.assertRaises(Rejected):
            self.model.open_channel(self.identity, "session", "127.0.0.1", 22, dial=forbidden)
    def test_stop_closes_everything_and_never_reopens(self):
        self.start()
        ch = self.open()
        self.model.revoke()
        self.assertTrue(ch.closed and self.transport.closed and self.process.closed)
        self.assertEqual(self.model.state, State.TERMINATED)
        for operation in (lambda: self.open(), lambda: self.model.renew(self.identity, 10),
                          lambda: self.model.mark_ready(self.identity), lambda: self.model.activate(self.identity)):
            with self.assertRaises(Rejected): operation()
    def test_lease_expiry_closes_existing(self):
        self.start()
        ch = self.open()
        self.advance(10)
        self.assertEqual(self.model.poll(), State.TERMINATED)
        self.assertTrue(ch.closed)
        self.assertEqual(self.model.evidence["reason"], "lease-expired")
    def test_renew_cannot_extend_authority_deadline(self):
        self.start()
        for seq in range(1, 8):
            self.advance(8)
            self.model.renew(self.identity, seq)
        self.advance(4)
        self.assertEqual(self.model.poll(), State.TERMINATED)
        self.assertEqual(self.model.evidence["reason"], "maximum-deadline")
    def test_stale_renew_and_namespace(self):
        self.start()
        self.model.renew(self.identity, 1)
        for seq in (1, 0, -1, True, 2.0):
            with self.assertRaises(Rejected): self.model.renew(self.identity, seq)
        with self.assertRaises(Rejected): self.model.renew(dataclasses.replace(self.identity, run_epoch="old"), 2)
        self.advance(10)
        with self.assertRaises(Rejected): self.model.renew(self.identity, 100)
        self.assertEqual(self.model.state, State.TERMINATED)
    def test_clock_backward_forward_suspend_and_invalid_fail_closed(self):
        for now in ([999, 1], [1001, -1], [9999, 1], [1001, 100], [float("nan"), 1]):
            self.setUp(); self.start(); ch = self.open()
            self.now[:] = now
            self.assertEqual(self.model.poll(), State.TERMINATED)
            self.assertTrue(ch.closed)
    def test_capacity_includes_pending_dial(self):
        self.start()
        def dial():
            with self.assertRaises(Rejected): self.open()
            return Handle()
        ch = self.open(dial=dial)
        with self.assertRaises(Rejected): self.open()
        self.model.close_channel(ch)
        self.assertTrue(ch.closed)
        self.open()
    def test_revoke_during_dial_and_failed_late_close(self):
        self.start(); ch = Handle(); ch.fail = True
        def dial():
            self.model.revoke()
            self.assertEqual(self.model.state, State.REVOKING)
            return ch
        with self.assertRaises(Rejected): self.open(dial=dial)
        self.assertEqual(self.model.state, State.REVOKING)
        self.assertEqual(self.model.evidence["unclosed_channels"], 1)
        self.assertEqual(self.model.evidence["close_errors"], ("OSError",))
        ch.fail = False
        self.assertEqual(self.model.poll(), State.TERMINATED)
        self.assertTrue(ch.closed)
    def test_owner_close_failure_retains_evidence(self):
        self.start(); self.process.fail = True
        self.model.revoke()
        self.assertEqual(self.model.state, State.REVOKING)
        self.assertEqual(self.model.evidence["unclosed_owners"], 1)
        self.process.fail = False
        self.assertEqual(self.model.poll(), State.TERMINATED)
    def test_dial_failure_revokes(self):
        self.start()
        def dial(): raise OSError("unavailable")
        with self.assertRaises(OSError): self.open(dial=dial)
        self.assertEqual(self.model.state, State.TERMINATED)
    def test_channel_close_failure_revokes(self):
        self.start(); ch = self.open(); ch.fail = True
        with self.assertRaises(OSError): self.model.close_channel(ch)
        self.assertEqual(self.model.state, State.REVOKING)
        self.assertEqual(self.model.evidence["unclosed_channels"], 1)
        ch.fail = False
        self.assertEqual(self.model.poll(), State.TERMINATED)
    def test_actual_thread_dial_race(self):
        self.start()
        entered, release = threading.Event(), threading.Event()
        ch = Handle(); errors = []
        def dial():
            entered.set()
            if not release.wait(2): raise RuntimeError("test timeout")
            return ch
        def worker():
            try: self.open(dial=dial)
            except Exception as exc: errors.append(exc)
        thread = threading.Thread(target=worker)
        thread.start()
        try:
            self.assertTrue(entered.wait(2))
            self.model.revoke()
            self.assertEqual(self.model.state, State.REVOKING)
        finally:
            release.set(); thread.join(2)
        self.assertFalse(thread.is_alive())
        self.assertEqual(len(errors), 1)
        self.assertIsInstance(errors[0], Rejected)
        self.assertTrue(ch.closed)
        self.assertEqual(self.model.state, State.TERMINATED)
    def test_clock_exception_and_malformed_read(self):
        for bad in (None, (1,), "invalid"):
            self.setUp(); self.start()
            self.model._clock = lambda: bad
            self.assertEqual(self.model.poll(), State.TERMINATED)
        self.setUp(); self.start()
        def failed_clock(): raise OSError("clock unavailable")
        self.model._clock = failed_clock
        self.assertEqual(self.model.poll(), State.TERMINATED)
    def test_authority_rejects_duration_outside_limits(self):
        for duration, lease in ((7201, 10), (60, 61), (float("inf"), 10), (True, 1)):
            with self.assertRaises(ValueError):
                OfflineSessionAuthority(self.policy, self.identity, clock=lambda: self.now,
                    process=Handle(), transport=Handle(), max_duration=duration, lease_duration=lease)
    def test_full_authority_denies_outer_shell(self):
        p = OperationPolicy("full")
        identity = SessionIdentity("launch", "epoch", p.digest)
        model = OfflineSessionAuthority(p, identity, clock=lambda: self.now,
                                        process=Handle(), transport=Handle())
        model.mark_ready(identity); model.activate(identity)
        for kind in ("shell", "exec", "session", "pty-req", "sftp", "tcpip-forward"):
            with self.assertRaises(Rejected):
                model.open_channel(identity, kind, "127.0.0.1", 22,
                                   dial=lambda: self.fail("must not dial"))
    def test_prepared_can_expire(self):
        self.advance(10)
        with self.assertRaises(Rejected): self.model.mark_ready(self.identity)
        self.assertEqual(self.model.state, State.TERMINATED)

if __name__ == "__main__": unittest.main()
