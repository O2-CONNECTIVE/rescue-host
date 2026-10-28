"""未接続評価ZIPを展開せず検証する。署名・実行・製品認可はしない。"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import zipfile
import zlib

from package_tauri_evaluation import NAME


class VerificationError(Exception):
    """入力内容を診断へ複製しない固定コード。"""


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        raise VerificationError("PACKAGE_ARGUMENT_INVALID")


def require(condition, code):
    if not condition:
        raise VerificationError(code)


def safe_member(name):
    # 現行生成物のASCII名に限定。Windowsの別名・予約名を展開前に拒否する。
    for part in name.split("/"):
        if (not re.fullmatch(r"[A-Za-z0-9_.+-]+", part) or part in {".", ".."}
                or part.endswith(".") or re.fullmatch(r"(?i)(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])",
                                                     part.split(".")[0])):
            return False
    return True


def check_licenses(bundle, prefix, files):
    code = "PACKAGE_LICENSE_INCOMPLETE"
    required = {"licenses/DEPENDENCIES.json", "licenses/FRONTEND_THIRD_PARTY_NOTICES.txt",
                "licenses/rust-toolchain/COPYRIGHT.html", "licenses/rust-toolchain/COPYRIGHT-library.html"}
    require(required <= set(files), code)
    license_files = {name for name in files if name.startswith("licenses/")}
    # 現行本文の形式のみ許可。inventoryへの追記だけでは実行物を許可しない。
    for name in license_files - required:
        file = Path(name)
        require(name.startswith("licenses/rust/") and
                (file.suffix.lower() in {"", ".txt", ".md", ".spdx", ".nuspec"}
                 or file.name.lower() in {"license.mit", "license.bsd-3-clause", "license-apache-2.0"}),
                "PACKAGE_COMPONENT_SET_UNSUPPORTED")
    for name in license_files:
        data = bundle.read(prefix + name)
        require(not data.startswith(b"MZ"), "PACKAGE_COMPONENT_SET_UNSUPPORTED")
        require(bool(data.decode("utf-8-sig").strip()), code)
    inventory = json.loads(bundle.read(prefix + "licenses/DEPENDENCIES.json"))
    require(inventory["tools"]["profile"] == "release" and bool(inventory["packages"]), code)
    notices = inventory["rustToolchainNotices"]
    require(set(notices) == {"COPYRIGHT.html", "COPYRIGHT-library.html"}, code)
    references = {"licenses/rust-toolchain/" + name: digest for name, digest in notices.items()}
    for package in inventory["packages"]:
        name = package["name"] + "-" + package["version"]
        require(re.fullmatch(r"[A-Za-z0-9_.+-]+", name) is not None and package["texts"]
                and package["source"] == "registry+https://github.com/rust-lang/crates.io-index"
                and re.fullmatch(r"[0-9a-f]{64}", package["crateChecksum"]) is not None, code)
        for file, digest in package["texts"].items():
            references["licenses/rust/" + name + "/" + file] = digest
    for file, digest in references.items():
        require(files.get(file) == digest and bool(bundle.read(prefix + file).strip()), code)
    require(license_files == required | set(references), "PACKAGE_COMPONENT_SET_UNSUPPORTED")
    require(bool(bundle.read(prefix + "licenses/FRONTEND_THIRD_PARTY_NOTICES.txt").strip()), code)


def verify(archive, source, archive_sha256, exe_sha256):
    require(re.fullmatch(r"[0-9a-f]{40}", source) is not None, "PACKAGE_EXPECTATION_INVALID")
    for digest in [archive_sha256, exe_sha256]:
        require(re.fullmatch(r"[0-9a-f]{64}", digest) is not None, "PACKAGE_EXPECTATION_INVALID")
    require(archive.is_file() and not archive.is_symlink(), "PACKAGE_ARCHIVE_INVALID")
    require(archive.stat().st_size <= 64 * 1024 * 1024, "PACKAGE_SIZE_LIMIT")
    with archive.open("rb") as stream:
        data = stream.read(64 * 1024 * 1024 + 1)
    require(len(data) <= 64 * 1024 * 1024, "PACKAGE_SIZE_LIMIT")
    require(hashlib.sha256(data).hexdigest() == archive_sha256, "PACKAGE_ARCHIVE_HASH_MISMATCH")
    # hashと検証を同じ取得bytesへ適用。以降にpathを再読込しない。
    with zipfile.ZipFile(io.BytesIO(data)) as bundle:
        infos = bundle.infolist()
        require(len(infos) <= 4096 and sum(i.file_size for i in infos) <= 128 * 1024 * 1024
                and all(i.file_size <= 32 * 1024 * 1024 for i in infos), "PACKAGE_SIZE_LIMIT")
        require(all(safe_member(i.filename) for i in infos), "PACKAGE_MEMBER_PATH_INVALID")
        require(len({i.filename.casefold() for i in infos}) == len(infos)
                and all((i.external_attr >> 16) & 0o170000 in {0, 0o100000}
                        and not i.flag_bits & 1 for i in infos), "PACKAGE_MEMBER_AMBIGUOUS")
        prefix = NAME + "/"
        manifest = json.loads(bundle.read(prefix + "MANIFEST.json"))
        fields = {"sourceRepository", "sourceCommit", "candidate", "platform", "buildProfile",
                  "integration", "canStart", "canStop", "productionGateClosed", "signature",
                  "webview2", "nativeValidation", "files"}
        require(isinstance(manifest, dict) and set(manifest) == fields,
                "PACKAGE_MANIFEST_UNSUPPORTED")
        require(manifest["sourceRepository"] == "O2-CONNECTIVE/rescue-host"
                and manifest["candidate"] == NAME and manifest["platform"] == "windows-x64"
                and manifest["buildProfile"] == "release" and manifest["signature"] == "unsigned",
                "PACKAGE_MANIFEST_UNSUPPORTED")
        require(manifest["sourceCommit"] == source, "PACKAGE_SOURCE_MISMATCH")
        require(manifest["integration"] == "not_connected" and manifest["canStart"] is False
                and manifest["canStop"] is False and manifest["productionGateClosed"] is True,
                "PACKAGE_BOUNDARY_MISMATCH")
        files = manifest["files"]
        expected = {prefix + name for name in files} | {prefix + "MANIFEST.json"}
        require(set(bundle.namelist()) == expected, "PACKAGE_MEMBER_SET_MISMATCH")
        fixed = {"rescue-host-shell.exe", "LICENSE", "README_JA.txt",
                 "locks/Cargo.lock", "locks/package-lock.json"}
        require(fixed <= set(files) and all(name in fixed or name.startswith("licenses/")
                for name in files), "PACKAGE_COMPONENT_SET_UNSUPPORTED")
        for name, digest in files.items():
            require(hashlib.sha256(bundle.read(prefix + name)).hexdigest() == digest,
                    "PACKAGE_FILE_HASH_MISMATCH")
        require(files["rescue-host-shell.exe"] == exe_sha256, "PACKAGE_EXE_HASH_MISMATCH")
        check_licenses(bundle, prefix, files)
    return {"check": "tauri-unwired-package", "status": "pass", "sourceCommit": source,
            "archiveSha256": archive_sha256, "exeSha256": exe_sha256, "fileCount": len(files),
            "backend": "absent", "productionGateClosed": True}


def main():
    parser = SafeParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--archive-sha256", required=True)
    parser.add_argument("--exe-sha256", required=True)
    try:
        args = parser.parse_args()
        report = verify(args.archive, args.source, args.archive_sha256, args.exe_sha256)
    except VerificationError as error:
        report = {"check": "tauri-unwired-package", "status": "fail", "code": str(error)}
    except (OSError, ValueError, KeyError, TypeError, AttributeError, RuntimeError,
            NotImplementedError, zipfile.BadZipFile, zlib.error):
        report = {"check": "tauri-unwired-package", "status": "fail", "code": "PACKAGE_INVALID"}
    print(json.dumps(report, ensure_ascii=True, sort_keys=True))
    return 0 if report["status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
