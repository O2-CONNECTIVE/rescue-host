use serde::{Deserialize, Serialize};

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
    // Safe red phase: the public IPC test must fail until the narrow adapter exists.
    Err("SHELL_NOT_IMPLEMENTED")
}

fn commands<R: tauri::Runtime>(builder: tauri::Builder<R>) -> tauri::Builder<R> {
    builder.invoke_handler(tauri::generate_handler![get_shell_status])
}

pub fn run() {
    commands(tauri::Builder::default())
        .setup(|app| {
            tauri::WebviewWindowBuilder::new(app, "main", tauri::WebviewUrl::App("index.html".into()))
                .title("Rescue Host — 未配線 shell")
                .inner_size(900.0, 780.0)
                .min_inner_size(360.0, 480.0)
                .on_navigation(|url| {
                    (url.scheme() == "tauri" && url.host_str() == Some("localhost"))
                        || (matches!(url.scheme(), "http" | "https")
                            && url.host_str() == Some("tauri.localhost"))
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

    #[test]
    fn public_ipc_reports_unwired_and_cannot_start_or_confirm_termination() {
        let app = commands(tauri::test::mock_builder())
            .build(tauri::generate_context!())
            .unwrap();
        let window = tauri::WebviewWindowBuilder::new(&app, "main", Default::default())
            .build()
            .unwrap();
        let response = tauri::test::get_ipc_response(
            &window,
            tauri::webview::InvokeRequest {
                cmd: "get_shell_status".into(),
                callback: tauri::ipc::CallbackFn(0),
                error: tauri::ipc::CallbackFn(1),
                url: "http://tauri.localhost".parse().unwrap(),
                body: tauri::ipc::InvokeBody::Json(json!({"request": {"protocolVersion": 1}})),
                headers: Default::default(),
                invoke_key: tauri::test::INVOKE_KEY.into(),
            },
        )
        .expect("status IPC must return the unwired contract")
        .deserialize::<serde_json::Value>()
        .unwrap();
        assert_eq!(response, json!({
            "protocolVersion": 1, "integration": "not_connected",
            "canStart": false, "canStop": false, "termination": "unconfirmed"
        }));
    }
}
