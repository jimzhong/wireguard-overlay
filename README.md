# wireguard-mesh

## How it works

A mesh consists of many nodes. One of them runs as a server and the others run as clients. When a client starts, it fetches the latest IPs of all clients from the server and then adds them as its wireguard peers. So eventually all clients will have peer-to-peer wireguard sessions.

The server must know the public keys of all clients. But clients only need to know the public key of the server because clients will fetch the public keys of all clients from the server. To guard against a compromised server, clients may use a preshared symmetric encryption on their wireguard sessions.