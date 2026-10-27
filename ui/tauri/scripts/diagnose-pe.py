"""Temporary CI-only import probe; remove after loader diagnosis."""
import ctypes
from pathlib import Path
import struct

files = sorted(Path("src-tauri/target/debug/deps").glob("rescue_host_shell-*.exe"))
assert files, "No owned shell test executable to inspect"
for file in files:
    data = file.read_bytes()
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    optional = pe + 24
    bits = 64 if struct.unpack_from("<H", data, optional)[0] == 0x20B else 32
    directory = optional + (112 if bits == 64 else 96)
    import_rva = struct.unpack_from("<I", data, directory + 8)[0]
    count = struct.unpack_from("<H", data, pe + 6)[0]
    section = optional + struct.unpack_from("<H", data, pe + 20)[0]
    sections = [struct.unpack_from("<IIII", data, section + 40 * i + 8) for i in range(count)]

    def offset(rva):
        for size, address, raw_size, raw_pointer in sections:
            if address <= rva < address + max(size, raw_size):
                return raw_pointer + rva - address
        raise ValueError("unmapped import RVA")

    def string_at(rva):
        start = offset(rva)
        return data[start:data.index(b"\0", start)].decode("ascii")

    descriptor = offset(import_rva)
    missing = []
    imports = 0
    while any(data[descriptor:descriptor + 20]):
        original, _, _, name, first = struct.unpack_from("<IIIII", data, descriptor)
        dll_name = string_at(name)
        try:
            library = ctypes.WinDLL(dll_name, winmode=0x800)
        except OSError as error:
            missing.append((dll_name, "LOAD_ERROR", error.winerror))
            descriptor += 20
            continue
        thunk = offset(original or first)
        while True:
            value = struct.unpack_from("<Q" if bits == 64 else "<I", data, thunk)[0]
            if not value:
                break
            name_or_ordinal = value & 0xFFFF if value & (1 << (bits - 1)) else string_at(value + 2)
            imports += 1
            try:
                library[name_or_ordinal] if isinstance(name_or_ordinal, int) else getattr(library, name_or_ordinal)
            except AttributeError:
                missing.append((dll_name, name_or_ordinal, "EXPORT_MISSING"))
            thunk += bits // 8
        descriptor += 20
    print("[PE-IMPORT]", file.name, "checked", imports, "imports; missing", missing)
