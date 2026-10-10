#!/usr/bin/env python3
"""A TLS client that stops after its ClientHello, for the proxy specs. Run inside an agent container:

    python3 tls_client.py CONNECT_HOST [--sni NAME | --no-sni] [--ech] [--pipelined]

It opens a tunnel through the proxy named by HTTPS_PROXY (with the agent's credentials) to CONNECT_HOST:443, sends one
ClientHello and prints what came back as a single line:

    refused <status>      the proxy answered the CONNECT with an error status
    server-hello          a TLS server answered with a ServerHello (the proxy terminated TLS, or the host did)
    alert                 a TLS alert
    closed                the connection was closed with no TLS data

--sni sets the server name (default: CONNECT_HOST), --no-sni leaves it out, --ech adds an Encrypted Client Hello
extension of the shape Chromium sends as GREASE (type outer, random config id and key, padded payload), and
--pipelined sends the hello in the same write as the CONNECT request, before the proxy has answered.
"""

import argparse
import base64
import os
import socket
import struct
import sys
import urllib.parse


def extension(kind, body):
    return struct.pack(">HH", kind, len(body)) + body


def client_hello(sni, ech):
    extensions = b""
    if sni:
        name = sni.encode()
        extensions += extension(0x0000, struct.pack(">H", len(name) + 3) + b"\x00" + struct.pack(">H", len(name)) + name)
    extensions += extension(0x000A, struct.pack(">HH", 2, 0x001D))  # supported_groups: x25519
    extensions += extension(0x000D, struct.pack(">HHHH", 6, 0x0403, 0x0804, 0x0807))  # signature_algorithms: ecdsa, rsa-pss, ed25519
    extensions += extension(0x002B, b"\x02\x03\x04")  # supported_versions: TLS 1.3
    extensions += extension(0x0033, struct.pack(">HHH", 36, 0x001D, 32) + os.urandom(32))  # key_share
    if ech:
        # type outer, HKDF-SHA256 / AES-128-GCM, a random config id, a 32 byte key, a 176 byte payload
        body = b"\x00" + struct.pack(">HH", 1, 1) + os.urandom(1) + struct.pack(">H", 32) + os.urandom(32)
        body += struct.pack(">H", 176) + os.urandom(176)
        extensions += extension(0xFE0D, body)
    hello = b"\x03\x03" + os.urandom(32) + b"\x20" + os.urandom(32)
    hello += struct.pack(">H", 2) + b"\x13\x01" + b"\x01\x00" + struct.pack(">H", len(extensions)) + extensions
    handshake = b"\x01" + struct.pack(">I", len(hello))[1:] + hello
    return b"\x16\x03\x01" + struct.pack(">H", len(handshake)) + handshake


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("host")
    parser.add_argument("--sni")
    parser.add_argument("--no-sni", action="store_true")
    parser.add_argument("--ech", action="store_true")
    parser.add_argument("--pipelined", action="store_true")
    arguments = parser.parse_args()

    proxy = urllib.parse.urlparse(os.environ["HTTPS_PROXY"])
    login = base64.b64encode(f"{urllib.parse.unquote(proxy.username)}:{urllib.parse.unquote(proxy.password)}".encode()).decode()
    sni = None if arguments.no_sni else (arguments.sni or arguments.host)
    hello = client_hello(sni, arguments.ech)
    connect = (
        f"CONNECT {arguments.host}:443 HTTP/1.1\r\nHost: {arguments.host}:443\r\nProxy-Authorization: Basic {login}\r\n\r\n"
    ).encode()

    connection = socket.create_connection((proxy.hostname, proxy.port), timeout=10)
    connection.sendall(connect + hello if arguments.pipelined else connect)
    received = b""
    while b"\r\n\r\n" not in received:
        chunk = connection.recv(4096)
        if not chunk:
            print("closed")
            return
        received += chunk
    head, _, rest = received.partition(b"\r\n\r\n")
    status = head.split(b" ", 2)[1].decode()
    if status != "200":
        print(f"refused {status}")
        return
    if not arguments.pipelined:
        connection.sendall(hello)
    connection.settimeout(8)
    try:
        while len(rest) < 6:
            chunk = connection.recv(4096)
            if not chunk:
                break
            rest += chunk
    except OSError:
        pass
    if len(rest) >= 6 and rest[0] == 0x16 and rest[5] == 0x02:
        print("server-hello")
    elif rest[:1] == b"\x15":
        print("alert")
    else:
        print("closed")


if __name__ == "__main__":
    sys.exit(main())
