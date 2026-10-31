#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What an agent's dev server does to its connections, one behaviour per port, on loopback only.

    forward-server.py BASE

BASE    echoes, then after the client's end of input says how many bytes it read and their SHA-256, then closes
BASE+1  says "bye" at once and closes: the server ends the connection
BASE+2  answers an HTTP Upgrade with 101, then echoes: a WebSocket as a proxy sees one
BASE+3  never reads or closes, and says how many connections it saw go: a server that ignores the client leaving
"""

import hashlib
import socket
import socketserver
import sys
import threading


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        digest, total = hashlib.sha256(), 0
        while data := self.request.recv(1 << 16):
            digest.update(data)
            total += len(data)
            self.request.sendall(data)
        self.request.sendall(f"\n{total} {digest.hexdigest()}\n".encode())


class Bye(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.sendall(b"bye\n")


class Upgrade(socketserver.BaseRequestHandler):
    def handle(self):
        request = b""
        while b"\r\n\r\n" not in request:
            request += self.request.recv(4096)
        self.request.sendall(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
        while data := self.request.recv(1 << 16):
            self.request.sendall(data)


class Deaf(socketserver.BaseRequestHandler):
    def handle(self):
        threading.Event().wait()


base = int(sys.argv[1])
for offset, handler in enumerate((Echo, Bye, Upgrade, Deaf)):
    server = Server(("127.0.0.1", base + offset), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
print("listening", flush=True)
threading.Event().wait()
