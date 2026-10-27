"""固定 Cargo metadata の crate と frontend notice を配布用に収集する。"""

import argparse
import hashlib
import json
from pathlib import Path
import re


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--frontend-notice", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    metadata = json.loads(args.metadata.read_text(encoding="utf-8-sig"))
    collected = {}
    packages = []
    for package in metadata["packages"]:
        if package["source"] is None:
            continue
        if package["source"] != "registry+https://github.com/rust-lang/crates.io-index":
            raise ValueError("公式 crates.io 以外の source は収集しません")
        name = package["name"] + "-" + package["version"]
        if not re.fullmatch(r"[A-Za-z0-9_.+-]+", name) or not package.get("license"):
            raise ValueError("crate の名前・license metadata が不明です")
        root = Path(package["manifest_path"]).parent
        candidates = [file for file in root.iterdir()
                      if re.match(r"(?i)^(licen[cs]e|copying|notice|copyright)([._-]|$)", file.name)]
        if package.get("license_file"):
            candidates.append(root / package["license_file"])
        texts = {}
        has_license = False
        for candidate in sorted(set(candidates)):
            files = list(candidate.rglob("*")) if candidate.is_dir() else [candidate]
            for file in files:
                if file.is_symlink() or not file.resolve().is_relative_to(root.resolve()):
                    raise ValueError("crate license は source root 内の通常 file だけです")
                if file.is_file():
                    data = file.read_bytes()
                    if not data.strip():
                        raise ValueError("空の license は使いません")
                    key = file.relative_to(root).as_posix()
                    texts[key] = hashlib.sha256(data).hexdigest()
                    collected["rust/" + name + "/" + key] = data
                    if re.match(r"(?i)^(licen[cs]e|copying)", candidate.name) or (
                            package.get("license_file") and candidate == root / package["license_file"]):
                        has_license = True
        if not has_license:
            raise ValueError("CRATE_LICENSE_MISSING:" + name)
        packages.append({"name": package["name"], "version": package["version"],
                         "source": package["source"], "license": package["license"], "texts": texts})
    if not packages:
        raise ValueError("crate 0 件を成功にしません")
    collected["FRONTEND_THIRD_PARTY_NOTICES.txt"] = args.frontend_notice.read_bytes()
    collected["DEPENDENCIES.json"] = (json.dumps(
        {"scope": "Cargo metadata 全 registry package。build/dev/非対象 OS を含み、linked SBOM ではない。",
         "tools": {"rust": "1.99.1", "node": "24.22.1", "profile": "release"},
         "packages": sorted(packages, key=lambda item: (item["name"], item["version"]))},
        ensure_ascii=False, indent=2) + "\n").encode()
    args.output.mkdir(parents=True, exist_ok=False)
    for name, data in collected.items():
        destination = args.output / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
    print("CARGO_AND_FRONTEND_LICENSES_COLLECTED", len(packages))


if __name__ == "__main__":
    main()
