module github.com/polevpn/polevpn_client

go 1.14

require (
	github.com/polevpn/anyvalue v1.0.6
	github.com/polevpn/elog v1.1.0
	github.com/polevpn/netstack v1.10.3 // indirect
	github.com/polevpn/polevpn_core v1.0.12
	github.com/polevpn/water v1.0.3 // indirect
)

replace github.com/polevpn/polevpn_core => ../polevpn_core
