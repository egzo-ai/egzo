#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The servers an agent runs for the specs of local ports, on 0.0.0.0 as the instructions tell an agent to.

    local-server.py NAME HTTP_PORT TCP_PORT UDP_PORT

HTTP_PORT  answers every request with "http:NAME"
TCP_PORT   answers each line with "tcp:NAME:" and the line
UDP_PORT   answers each datagram with "udp:NAME:" and the datagram

NAME says which instance answered, so a spec can tell two instances apart.
"""

import socket
import socketserver
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

name = sys.argv[1]


class Web(BaseHTTPRequestHandler):
    def do_GET(self):
        body = f"http:{name}".encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class Lines(socketserver.StreamRequestHandler):
    def handle(self):
        for line in self.rfile:
            self.wfile.write(f"tcp:{name}:".encode() + line)
            self.wfile.flush()


class Tcp(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def datagrams(port):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", port))
    while True:
        data, peer = sock.recvfrom(65535)
        sock.sendto(f"udp:{name}:".encode() + data, peer)


ThreadingHTTPServer.daemon_threads = True
threading.Thread(target=ThreadingHTTPServer(("0.0.0.0", int(sys.argv[2])), Web).serve_forever, daemon=True).start()
threading.Thread(target=Tcp(("0.0.0.0", int(sys.argv[3])), Lines).serve_forever, daemon=True).start()
datagrams(int(sys.argv[4]))
