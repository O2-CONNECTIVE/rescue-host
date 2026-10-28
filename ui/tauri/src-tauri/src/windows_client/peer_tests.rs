use super::peer::{bind, ExpectedServer, PeerCalls};
use super::Fault;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::System::Threading::{
    PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
};

struct DifferentUser {
    closed: AtomicUsize,
}

impl PeerCalls for DifferentUser {
    fn pipe_server_pid(&self, _: HANDLE) -> Result<u32, Fault> {
        Ok(42)
    }
    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault> {
        assert_eq!(pid, 42);
        let rights = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
        assert_eq!(access, rights);
        assert!(!inherit);
        Ok(std::ptr::without_provenance_mut(7))
    }
    fn process_id(&self, _: HANDLE) -> Result<u32, Fault> {
        Ok(42)
    }
    fn created(&self, _: HANDLE) -> Result<u64, Fault> {
        Ok(100)
    }
    fn token_user(&self, _: HANDLE) -> Result<String, Fault> {
        Ok("S-1-5-80-11-22-33-44-56".into())
    }
    fn alive(&self, _: HANDLE) -> Result<bool, Fault> {
        Ok(true)
    }
    fn close(&self, handle: HANDLE) {
        assert_eq!(handle, std::ptr::without_provenance_mut(7));
        self.closed.fetch_add(1, Ordering::SeqCst);
    }
    fn fatal(&self) -> ! {
        panic!("fake peer fatal")
    }
}

#[test]
fn a_different_primary_token_user_is_rejected_and_the_process_handle_is_closed() {
    let api = Arc::new(DifferentUser {
        closed: AtomicUsize::new(0),
    });
    let result = bind(
        Arc::clone(&api),
        std::ptr::without_provenance_mut(1),
        ExpectedServer {
            pid: 42,
            created: 100,
            service_sid: "S-1-5-80-11-22-33-44-55".into(),
        },
    );
    assert!(
        matches!(result, Err(Fault::Failed)),
        "a different TokenUser must not bind as the expected service"
    );
    assert_eq!(api.closed.load(Ordering::SeqCst), 1);
}

fn expected() -> ExpectedServer {
    ExpectedServer {
        pid: 42,
        created: 100,
        service_sid: "S-1-5-80-11-22-33-44-55".into(),
    }
}

struct Facts {
    pipe_pid: u32,
    process_pid: u32,
    created: u64,
    user: Result<String, Fault>,
    alive: bool,
    pipe_after_user: Option<u32>,
    exits_after_user: bool,
}

fn matching() -> Facts {
    Facts {
        pipe_pid: 42,
        process_pid: 42,
        created: 100,
        user: Ok(expected().service_sid),
        alive: true,
        pipe_after_user: None,
        exits_after_user: false,
    }
}

struct FakePeer {
    // Reuse the original RED seam's exact rights and exact handle-close checks.
    handles: DifferentUser,
    facts: Mutex<Facts>,
    opened: AtomicUsize,
    queried: AtomicUsize,
}

impl FakePeer {
    fn new(facts: Facts) -> Arc<Self> {
        Arc::new(Self {
            handles: DifferentUser {
                closed: AtomicUsize::new(0),
            },
            facts: Mutex::new(facts),
            opened: AtomicUsize::new(0),
            queried: AtomicUsize::new(0),
        })
    }

    fn held(process: HANDLE) {
        assert_eq!(process, std::ptr::without_provenance_mut(7));
    }
}

impl PeerCalls for FakePeer {
    fn pipe_server_pid(&self, pipe: HANDLE) -> Result<u32, Fault> {
        assert_eq!(pipe, std::ptr::without_provenance_mut(1));
        self.queried.fetch_add(1, Ordering::SeqCst);
        Ok(self.facts.lock().unwrap().pipe_pid)
    }

    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault> {
        self.opened.fetch_add(1, Ordering::SeqCst);
        self.handles.open_process(pid, access, inherit)
    }

    fn process_id(&self, process: HANDLE) -> Result<u32, Fault> {
        Self::held(process);
        Ok(self.facts.lock().unwrap().process_pid)
    }

    fn created(&self, process: HANDLE) -> Result<u64, Fault> {
        Self::held(process);
        Ok(self.facts.lock().unwrap().created)
    }

    fn token_user(&self, process: HANDLE) -> Result<String, Fault> {
        Self::held(process);
        let mut facts = self.facts.lock().unwrap();
        let result = facts.user.clone();
        if let Some(pid) = facts.pipe_after_user.take() {
            facts.pipe_pid = pid;
        }
        if facts.exits_after_user {
            facts.alive = false;
        }
        result
    }

    fn alive(&self, process: HANDLE) -> Result<bool, Fault> {
        Self::held(process);
        Ok(self.facts.lock().unwrap().alive)
    }

    fn close(&self, handle: HANDLE) {
        self.handles.close(handle);
    }

    fn fatal(&self) -> ! {
        self.handles.fatal()
    }
}

#[test]
fn invalid_bootstrap_values_are_rejected_before_any_kernel_query_or_open() {
    let mut cases = vec![
        ExpectedServer {
            pid: 0,
            ..expected()
        },
        ExpectedServer {
            created: 0,
            ..expected()
        },
    ];
    for sid in [
        "",
        "S-1-5-18",
        "S-1-1-0",
        "S-1-5-11",
        "S-1-5-80-0",
        "S-1-5-80-11-22-33-44",
        "S-1-5-80-11-22-33-44-055",
        "S-1-5-21-1-2-3-4294967296",
        "S-1-5-21-1-2-3-4-extra",
    ] {
        cases.push(ExpectedServer {
            service_sid: sid.into(),
            ..expected()
        });
    }
    assert_eq!(cases.len(), 11);
    for expected in cases {
        let api = FakePeer::new(matching());
        assert_eq!(expected.validate(), Err(Fault::Failed));
        assert!(matches!(
            bind(Arc::clone(&api), std::ptr::without_provenance_mut(1), expected),
            Err(Fault::Failed)
        ));
        assert_eq!(api.queried.load(Ordering::SeqCst), 0);
        assert_eq!(api.opened.load(Ordering::SeqCst), 0);
        assert_eq!(api.handles.closed.load(Ordering::SeqCst), 0);
    }
}

#[test]
fn bind_rejects_identity_query_failures_and_closes_only_the_acquired_handle() {
    let cases = [
        (
            Facts {
                pipe_pid: 43,
                ..matching()
            },
            0,
        ),
        (
            Facts {
                process_pid: 43,
                ..matching()
            },
            1,
        ),
        (
            Facts {
                created: 101,
                ..matching()
            },
            1,
        ),
        (
            Facts {
                user: Err(Fault::Failed),
                ..matching()
            },
            1,
        ),
        (
            Facts {
                alive: false,
                ..matching()
            },
            1,
        ),
        (
            Facts {
                pipe_after_user: Some(43),
                ..matching()
            },
            1,
        ),
        (
            Facts {
                exits_after_user: true,
                ..matching()
            },
            1,
        ),
    ];
    assert_eq!(cases.len(), 7);
    for (facts, opened) in cases {
        let api = FakePeer::new(facts);
        assert!(matches!(
            bind(
                Arc::clone(&api),
                std::ptr::without_provenance_mut(1),
                expected()
            ),
            Err(Fault::Failed)
        ));
        assert_eq!(api.opened.load(Ordering::SeqCst), opened);
        assert_eq!(api.handles.closed.load(Ordering::SeqCst), opened);
    }
}

#[test]
fn canonical_service_or_account_user_binds_without_closing_the_retained_handle() {
    for sid in ["S-1-5-80-11-22-33-44-55", "S-1-5-21-101-102-103-1001"] {
        let api = FakePeer::new(Facts {
            user: Ok(sid.into()),
            ..matching()
        });
        let peer = bind(
            Arc::clone(&api),
            std::ptr::without_provenance_mut(1),
            ExpectedServer {
                service_sid: sid.into(),
                ..expected()
            },
        )
        .unwrap_or_else(|_| panic!("matching canonical TokenUser must bind"));
        assert_eq!(api.opened.load(Ordering::SeqCst), 1);
        assert_eq!(api.handles.closed.load(Ordering::SeqCst), 0);
        assert_eq!(peer.check(std::ptr::without_provenance_mut(1)), Ok(()));
        assert_eq!(api.opened.load(Ordering::SeqCst), 1);
        drop(peer);
        assert_eq!(api.handles.closed.load(Ordering::SeqCst), 1);
    }
}

#[test]
fn a_failed_recheck_retains_the_same_handle_until_its_outer_owner_drops() {
    let api = FakePeer::new(matching());
    let peer = bind(
        Arc::clone(&api),
        std::ptr::without_provenance_mut(1),
        expected(),
    )
    .unwrap_or_else(|_| panic!("initial matching peer must bind"));
    api.facts.lock().unwrap().created = 101;
    assert_eq!(
        peer.check(std::ptr::without_provenance_mut(1)),
        Err(Fault::Failed)
    );
    assert_eq!(api.opened.load(Ordering::SeqCst), 1);
    assert_eq!(api.handles.closed.load(Ordering::SeqCst), 0);
    drop(peer);
    assert_eq!(api.handles.closed.load(Ordering::SeqCst), 1);
}
