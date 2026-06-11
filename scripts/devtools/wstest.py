import socket, ssl, base64, os, json, sys

def ws_connect(host, path="/ws", port=443):
    ctx = ssl.create_default_context()
    sock = socket.create_connection((host, port), timeout=10)
    ssock = ctx.wrap_socket(sock, server_hostname=host)
    key = base64.b64encode(os.urandom(16)).decode()
    handshake = f"GET {path} HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
    ssock.send(handshake.encode())
    resp = ssock.recv(4096).decode()
    if "101" not in resp:
        print("Handshake failed:", resp[:200])
        return None
    return ssock

def ws_send(sock, msg):
    data = msg.encode(); length = len(data); mask = os.urandom(4)
    frame = bytearray([0x81, 0x80 | length]) + bytearray(mask)
    frame += bytearray(b ^ mask[i % 4] for i, b in enumerate(data))
    sock.send(bytes(frame))

def ws_recv(sock):
    header = sock.recv(2)
    if len(header) < 2: return None
    length = header[1] & 0x7f
    if length == 126: length = int.from_bytes(sock.recv(2), 'big')
    elif length == 127: length = int.from_bytes(sock.recv(8), 'big')
    data = b""
    while len(data) < length:
        chunk = sock.recv(length - len(data))
        if not chunk: break
        data += chunk
    return data.decode()

coin = sys.argv[1] if len(sys.argv) > 1 else "BTC"
print(f"Testing activeAssetCtx for coin={coin}")
sock = ws_connect("api.hyperliquid.xyz")
if not sock:
    sys.exit(1)
ws_send(sock, json.dumps({"method":"subscribe","subscription":{"type":"activeAssetCtx","coin":coin}}))
sock.settimeout(5)
for i in range(3):
    try:
        msg = ws_recv(sock)
        print("Recv:", msg[:300] if msg else "None")
    except socket.timeout:
        print("Timeout (no more messages in 5s)")
        break
    except Exception as e:
        print("Error:", e)
        break
