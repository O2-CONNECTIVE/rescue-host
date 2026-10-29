"""配布CLI用の有限license fixture。実依存の完全性や許諾の証拠ではない。"""

import hashlib
import json


def write_license_fixture(licenses):
    texts = {"FRONTEND_THIRD_PARTY_NOTICES.txt": b"fixture frontend",
             "rust-toolchain/COPYRIGHT.html": b"fixture compiler",
             "rust-toolchain/COPYRIGHT-library.html": b"fixture runtime",
             "rust/fixture-1.0.0/LICENSE": b"fixture license",
             "rust/fixture-1.0.0/LICENSE-APACHE-2.0": b"fixture apache license"}
    for name, data in texts.items():
        file = licenses / name
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_bytes(data)
    inventory = {"tools": {"profile": "release"}, "packages": [
        {"name": "fixture", "version": "1.0.0",
         "source": "registry+https://github.com/rust-lang/crates.io-index",
         "crateChecksum": "a" * 64,
         "texts": {"LICENSE": hashlib.sha256(b"fixture license").hexdigest(),
                   "LICENSE-APACHE-2.0": hashlib.sha256(b"fixture apache license").hexdigest()}}],
        "rustToolchainNotices": {
            name.removeprefix("rust-toolchain/"): hashlib.sha256(data).hexdigest()
            for name, data in texts.items() if name.startswith("rust-toolchain/")}}
    (licenses / "DEPENDENCIES.json").write_text(json.dumps(inventory), encoding="utf-8")
