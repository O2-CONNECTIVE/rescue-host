"""未配線 Tauri の内部評価用 ZIP を作る。実行・署名・公開はしない。"""

import hashlib
import json
from pathlib import Path
import re
import tempfile
import zipfile
import zlib


ROOT = Path(__file__).resolve().parents[1]
NAME = "RescueHostTauriUnwiredEvaluation"


def main():
    # validator は NAME を参照するため、CLI の開始後に読み込む。
    from verify_tauri_evaluation import SafeParser, VerificationError

    try:
        return package(SafeParser(description=__doc__))
    except VerificationError as error:
        print(str(error))
    except (OSError, ValueError, KeyError, TypeError, AttributeError, RuntimeError,
            NotImplementedError, zipfile.BadZipFile, zlib.error):
        print("PACKAGE_INVALID")
    return 1


def package(parser):
    from verify_tauri_evaluation import verify

    parser.add_argument("--exe", type=Path, required=True)
    parser.add_argument("--licenses", type=Path, required=True)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.source_sha):
        parser.error("完全な source SHA が必要です")
    if args.exe.is_symlink() or not args.exe.is_file() or args.exe.stat().st_size == 0:
        parser.error("通常の非空 EXE file が必要です")
    if args.licenses.is_symlink() or not args.licenses.is_dir():
        parser.error("通常の license directory が必要です")
    license_files = sorted(args.licenses.rglob("*"))
    if not any(file.is_file() for file in license_files) or any(file.is_symlink() for file in license_files):
        parser.error("license の欠落・symlink は配布候補にしません")
    files = {"rescue-host-shell.exe": args.exe.read_bytes(),
             "LICENSE": (ROOT / "LICENSE").read_bytes(),
             "README_JA.txt": (ROOT / "docs/tauri-evaluation-readme.txt").read_bytes(),
             "locks/Cargo.lock": (ROOT / "ui/tauri/src-tauri/Cargo.lock").read_bytes(),
             "locks/package-lock.json": (ROOT / "ui/tauri/package-lock.json").read_bytes()}
    for file in license_files:
        if file.is_file():
            files["licenses/" + file.relative_to(args.licenses).as_posix()] = file.read_bytes()
    manifest = {"sourceRepository": "O2-CONNECTIVE/rescue-host", "sourceCommit": args.source_sha,
                "candidate": NAME, "platform": "windows-x64", "buildProfile": "release",
                "integration": "not_connected", "canStart": False, "canStop": False,
                "productionGateClosed": True, "signature": "unsigned",
                "webview2": "既存の Microsoft WebView2 Runtime が必要。自動導入しない。",
                "nativeValidation": "別の CI 実測記録を確認。ZIP 生成だけでは成功にしない。",
                "files": {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}}
    files["MANIFEST.json"] = (json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode()
    with tempfile.TemporaryDirectory() as temporary:
        staged = Path(temporary) / (NAME + "-x64.zip")
        with zipfile.ZipFile(staged, "x", zipfile.ZIP_DEFLATED) as bundle:
            for name, data in sorted(files.items()):
                bundle.writestr(NAME + "/" + name, data)
        archive_bytes = staged.read_bytes()
        verify(staged, args.source_sha, hashlib.sha256(archive_bytes).hexdigest(),
               manifest["files"]["rescue-host-shell.exe"])
    args.output.mkdir(parents=True, exist_ok=False)
    archive = args.output / (NAME + "-x64.zip")
    archive.write_bytes(archive_bytes)
    archive.with_suffix(".zip.sha256").write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n", encoding="ascii")
    print("TAURI_UNWIRED_EVALUATION_ARCHIVE_CREATED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
