module polevpnmobile

go 1.14

require (
	github.com/polevpn/anyvalue v1.0.3
	github.com/polevpn/elog v1.0.6
	github.com/polevpn/poleclient v0.0.0-20201121080718-d6b5a70ada51
	golang.org/x/mobile v0.0.0-20200801112145-973feb4309de // indirect
)

replace (
	github.com/polevpn/poleclient => ../
)
