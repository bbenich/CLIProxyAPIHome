"""Export the complete Go embed.FS panel from the pinned upstream ELF binary.

The upstream image embeds its assets rather than shipping a static directory.
Fail closed if the pinned binary's Go embed layout is not recognized.
"""
import hashlib
from pathlib import Path, PurePosixPath
import struct
import sys


def export(binary, destination):
    blob = Path(binary).read_bytes()
    if blob[:6] != b'\x7fELF\x02\x01':
        raise ValueError('Expected a little-endian ELF64 upstream binary')
    phoff = struct.unpack_from('<Q', blob, 32)[0]
    phsize, phcount = struct.unpack_from('<HH', blob, 54)
    segments = []
    for i in range(phcount):
        kind, _, offset, address, _, size, _, _ = struct.unpack_from('<IIQQQQQQ', blob, phoff + i * phsize)
        if kind == 1:
            segments.append((address, address + size, offset))

    def offset_of(address, length):
        for lo, hi, offset in segments:
            if lo <= address and address + length <= hi:
                return offset + address - lo
        raise ValueError('Embedded pointer outside file-backed ELF segment')

    def record(offset):
        nameptr, namelen, dataptr, datalen = struct.unpack_from('<QQQQ', blob, offset)
        if not 6 <= namelen <= 256 or datalen > 16 * 1024 * 1024:
            raise ValueError('Invalid embedded record size')
        name = blob[offset_of(nameptr, namelen):][:namelen].decode('ascii')
        if not name.startswith('static/') or '..' in PurePosixPath(name).parts or '\\' in name:
            raise ValueError('Not a panel record')
        if name.endswith('/'):
            if datalen != 0:
                raise ValueError('Invalid directory record')
            return name, None
        data = blob[offset_of(dataptr, datalen):][:datalen] if datalen else b''
        # Go's compiler uses distinct hashes for small and streamed files.
        digest = bytearray(hashlib.sha256(data if len(data) <= 1024 else b'\x01' + data).digest())
        if len(data) <= 1024:
            digest[0] ^= 0xff
        if digest[:16] != blob[offset + 32:offset + 48]:
            raise ValueError('Embedded asset checksum mismatch')
        return name, data

    anchor = b'static/index.html'
    records = None
    start = 0
    while True:
        pos = blob.find(anchor, start)
        if pos < 0:
            break
        start = pos + 1
        address = next((lo + pos - offset for lo, hi, offset in segments if offset <= pos < offset + hi - lo), None)
        if address is None:
            continue
        marker = struct.pack('<QQ', address, len(anchor))
        table = blob.find(marker)
        while table >= 0:
            try:
                if record(table)[0] != anchor.decode():
                    raise ValueError('Wrong anchor')
                first = table
                while first >= 48:
                    try:
                        record(first - 48)
                    except (ValueError, UnicodeDecodeError, struct.error):
                        break
                    first -= 48
                pointer, count, capacity = struct.unpack_from('<QQQ', blob, first - 24)
                if not 40 <= count <= 2000 or count != capacity or offset_of(pointer, count * 48) != first:
                    raise ValueError('Invalid authoritative embed.FS slice header')
                found = {}
                for i in range(count):
                    name, data = record(first + i * 48)
                    if data is not None:
                        short = name.removeprefix('static/')
                        if short in found:
                            raise ValueError('Duplicate embedded asset')
                        found[short] = data
                if 'index.html' not in found or 'management.html' not in found:
                    raise ValueError('Missing upstream entrypoints')
                records = found
                break
            except (ValueError, UnicodeDecodeError, struct.error):
                table = blob.find(marker, table + 1)
        if records is not None:
            break
    if records is None:
        raise ValueError('Could not find a complete, checksum-valid upstream panel')
    out = Path(destination)
    for name, data in records.items():
        target = out / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    print(f'Exported {len(records)} checksum-verified upstream panel files')
    return records

if __name__ == '__main__':
    export(sys.argv[1], sys.argv[2])
