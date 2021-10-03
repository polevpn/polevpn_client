# poleclient

## config 
```
{
    "endpoint":"quic://127.0.0.2/h3",
    "skipVerifySSL":true,
    "user":"xxxx",
    "password":"xxxxx",
    "acls":["0.0.0.0/0"],
    "sni":"www.apple.com",
    "use_remote_route":false,
    "route_networks":["10.8.0.0/16","192.168.10.0/24"]
}
```

## windows client allow inbound traffic
* Windows Firewall-> Enable or Disable -> Disable Firewall (or configure firewall rules)