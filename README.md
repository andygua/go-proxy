# GO PROXY
A proxy between master and slave to simulate two problems
1. High latency
2. Frequent Disconnection

to Disconnect all the connections by this proxy to simulate network partition
```
curl http://127.0.0.1:7979/Disconnect/
```

to reallow connections (or to simulate network partition recovery)
```
curl http://127.0.0.1:7979/Connect/
```
