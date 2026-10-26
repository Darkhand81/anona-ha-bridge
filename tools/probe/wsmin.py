import socket,ssl,base64,os,struct
class WS:
    def __init__(self,url,timeout=20):
        assert url.startswith("wss://")
        hostport,_,path=url[6:].partition("/")
        host,_,port=hostport.partition(":")
        self.path="/"+path; self.host=host; self.port=int(port or 443)
        raw=socket.create_connection((host,self.port),timeout=timeout)
        ctx=ssl.create_default_context()   # verify certs + hostname (default)
        self.s=ctx.wrap_socket(raw,server_hostname=host); self.s.settimeout(timeout)
        key=base64.b64encode(os.urandom(16)).decode()
        req=(f"GET {self.path} HTTP/1.1\r\nHost: {host}:{self.port}\r\nUpgrade: websocket\r\n"
             f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n")
        self.s.sendall(req.encode())
        buf=b""
        while b"\r\n\r\n" not in buf: buf+=self.s.recv(4096)
        if b" 101 " not in buf.split(b"\r\n")[0]: raise RuntimeError("ws handshake failed: "+buf[:120].decode('latin1'))
        self.buf=buf.split(b"\r\n\r\n",1)[1]
    def send(self,data,opcode=0x1):
        if isinstance(data,str): data=data.encode()
        h=bytearray([0x80|opcode]); n=len(data); mask=os.urandom(4)
        if n<126: h.append(0x80|n)
        elif n<65536: h.append(0x80|126); h+=struct.pack(">H",n)
        else: h.append(0x80|127); h+=struct.pack(">Q",n)
        h+=mask; self.s.sendall(bytes(h)+bytes(b^mask[i%4] for i,b in enumerate(data)))
    def _read(self,n):
        while len(self.buf)<n: self.buf+=self.s.recv(4096)
        d,self.buf=self.buf[:n],self.buf[n:]; return d
    def recv(self):
        while True:
            b0,b1=self._read(2); op=b0&0x0f; ln=b1&0x7f
            if ln==126: ln=struct.unpack(">H",self._read(2))[0]
            elif ln==127: ln=struct.unpack(">Q",self._read(8))[0]
            payload=self._read(ln)
            if op==0x8: raise ConnectionError("ws closed by server")
            if op==0x9: self.send(payload,opcode=0xA); continue
            if op==0xA: continue
            return payload.decode("utf-8","replace")
    def close(self):
        try: self.s.close()
        except Exception: pass
