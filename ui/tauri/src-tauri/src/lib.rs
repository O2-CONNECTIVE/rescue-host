use serde::{Deserialize, Serialize};

#[allow(
    dead_code,
    reason = "control codec is not wired while runtime activation is closed"
)]
mod control_codec;

#[allow(
    dead_code,
    reason = "internal process boundary is not wired while the product gate is closed"
)]
mod owned_process;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct StatusRequest {
    protocol_version: u8,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ShellStatus {
    protocol_version: u8,
    integration: &'static str,
    can_start: bool,
    can_stop: bool,
    termination: &'static str,
}

#[tauri::command]
fn get_shell_status(request: StatusRequest) -> Result<ShellStatus, &'static str> {
    if request.protocol_version != 1 {
        return Err("UNSUPPORTED_SHELL_VERSION");
    }
    Ok(ShellStatus {
        protocol_version: 1,
        integration: "not_connected",
        can_start: false,
        can_stop: false,
        termination: "unconfirmed",
    })
}

fn commands<R: tauri::Runtime>(builder: tauri::Builder<R>) -> tauri::Builder<R> {
    builder.invoke_handler(tauri::generate_handler![get_shell_status])
}

pub fn run() {
    commands(tauri::Builder::default())
        .setup(|app| {
            tauri::WebviewWindowBuilder::new(
                app,
                "main",
                tauri::WebviewUrl::App("index.html".into()),
            )
            .title("Rescue Host（未配線 shell）")
            .inner_size(900.0, 780.0)
            .min_inner_size(360.0, 480.0)
            .on_navigation(|url| {
                if !url.username().is_empty() || url.password().is_some() || url.port().is_some() {
                    return false;
                }
                #[cfg(windows)]
                let bundled = url.scheme() == "http" && url.host_str() == Some("tauri.localhost");
                #[cfg(not(windows))]
                let bundled = url.scheme() == "tauri" && url.host_str() == Some("localhost");
                bundled
            })
            .build()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("failed to run Rescue Host shell");
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ipc(
        label: &str,
        origin: &str,
        command: &str,
        body: serde_json::Value,
    ) -> Result<serde_json::Value, serde_json::Value> {
        let app = commands(tauri::test::mock_builder())
            .build(tauri::generate_context!())
            .unwrap();
        let window = tauri::WebviewWindowBuilder::new(&app, label, Default::default())
            .build()
            .unwrap();
        tauri::test::get_ipc_response(
            &window,
            tauri::webview::InvokeRequest {
                cmd: command.into(),
                callback: tauri::ipc::CallbackFn(0),
                error: tauri::ipc::CallbackFn(1),
                url: origin.parse().unwrap(),
                body: tauri::ipc::InvokeBody::Json(body),
                headers: Default::default(),
                invoke_key: tauri::test::INVOKE_KEY.into(),
            },
        )
        .map(|response| response.deserialize::<serde_json::Value>().unwrap())
    }

    #[test]
    fn public_ipc_reports_unwired_and_cannot_start_or_confirm_termination() {
        let response = ipc(
            "main",
            "http://tauri.localhost",
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}}),
        )
        .unwrap();
        assert_eq!(
            response,
            json!({
                "protocolVersion": 1, "integration": "not_connected",
                "canStart": false, "canStop": false, "termination": "unconfirmed"
            })
        );
    }

    #[test]
    fn rejects_unsupported_version() {
        let error = ipc(
            "main",
            "http://tauri.localhost",
            "get_shell_status",
            json!({"request": {"protocolVersion": 2}}),
        )
        .unwrap_err();
        assert_eq!(error, json!("UNSUPPORTED_SHELL_VERSION"));
    }

    #[test]
    fn rejects_unknown_request_field_and_wrong_type() {
        for request in [
            json!({"protocolVersion": 1, "executable": "not-allowed"}),
            json!({"protocolVersion": "1"}),
        ] {
            assert!(ipc(
                "main",
                "http://tauri.localhost",
                "get_shell_status",
                json!({"request": request})
            )
            .is_err());
        }
    }

    #[test]
    fn denies_window_without_capability() {
        assert!(ipc(
            "other",
            "http://tauri.localhost",
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}})
        )
        .is_err());
    }

    #[test]
    fn denies_remote_origin() {
        assert!(ipc(
            "main",
            "https://example.invalid",
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}})
        )
        .is_err());
    }

    #[test]
    fn has_no_start_session_command() {
        assert!(ipc("main", "http://tauri.localhost", "start_session", json!({})).is_err());
    }

    #[test]
    fn shared_shell_v1_fixture_matches_public_mock_ipc() {
        let fixture_text = include_str!("../../contracts/shell-status-v1.json");
        let fixture: serde_json::Value = serde_json::from_str(fixture_text).unwrap();
        assert_eq!(fixture["scope"], "unwired-shell-status-v1");
        let command = fixture["command"].as_str().unwrap();
        let response = ipc(
            "main",
            "http://tauri.localhost",
            command,
            json!({"request": fixture["request"]}),
        )
        .unwrap();
        assert_eq!(response, fixture["response"]);
        let cases = fixture["rejectedRequests"].as_array().unwrap();
        assert_eq!(cases.len(), 8);
        let mut ids = std::collections::HashSet::new();
        for case in cases {
            let id = case["id"].as_str().unwrap();
            assert!(!id.is_empty());
            assert!(ids.insert(id));
            assert!(case.get("payload").is_some());
            assert!(
                ipc(
                    "main",
                    "http://tauri.localhost",
                    command,
                    json!({"request": case["payload"]}),
                )
                .is_err(),
                "{id}"
            );
        }
    }
}
