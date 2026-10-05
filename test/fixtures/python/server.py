"""No OpenTelemetry code here on purpose: spans in Tsuga prove injection worked."""
import json
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = 8080


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        print(json.dumps({"level": "info", "msg": "handled", "path": self.path}), flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"ok\n")

    def log_message(self, *_args):
        pass


def drive():
    while True:
        time.sleep(2)
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{PORT}/work", timeout=5).read()
        except Exception as err:  # noqa: BLE001 - a failed self-call must not kill the driver
            print(json.dumps({"level": "warn", "msg": str(err)}), flush=True)


if __name__ == "__main__":
    threading.Thread(target=drive, daemon=True).start()
    print(json.dumps({"level": "info", "msg": "listening", "port": PORT}), flush=True)
    HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
