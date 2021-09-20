package main

import (
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	core "github.com/polevpn/polevpn_core"
)

var endpoint string
var user string
var pwd string
var sni string
var verifySSL bool
var plog *elog.EasyLogger

func init() {
	flag.StringVar(&endpoint, "e", "", "server connect to")
	flag.StringVar(&user, "u", "", "login user")
	flag.StringVar(&pwd, "p", "", "login password")
	flag.StringVar(&sni, "s", "www.apple.com", "fake domain")
	flag.BoolVar(&verifySSL, "v", false, "verify tls certificate")

	plog = elog.GetLogger()
}

func signalHandler(pc *core.PoleVpnClient) {

	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range c {
			switch s {
			case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				plog.Info("receive exit signal,exit")
				if pc != nil {
					pc.Stop()
				}
				plog.Flush()
				os.Exit(0)
			case syscall.SIGUSR1:
			case syscall.SIGUSR2:
			default:
			}
		}
	}()
}

var device *core.TunDevice
var networkmgr NetworkManager

func eventHandler(event int, client *core.PoleVpnClient, av *anyvalue.AnyValue) {

	switch event {
	case core.CLIENT_EVENT_ADDRESS_ALLOCED:
		{
			err := networkmgr.SetNetwork(device.GetInterface().Name(), av.Get("ip").AsStr(), client.GetRemoteIP(), av.Get("dns").AsStr(), av.Get("route").AsStrArr())
			if err != nil {
				plog.Error("set network fail,", err)
				client.Stop()
			}
		}
	case core.CLIENT_EVENT_STOPPED:
		{
			plog.Info("client stoped")
			networkmgr.RestoreNetwork()
		}
	case core.CLIENT_EVENT_RECONNECTED:
		plog.Info("client reconnected")
	case core.CLIENT_EVENT_RECONNECTING:
		networkmgr.RefreshDefaultGateway()
		plog.Info("client reconnecting")
	case core.CLIENT_EVENT_STARTED:
		plog.Info("client started")
	case core.CLIENT_EVENT_ERROR:
		plog.Info("client error ", av.Get("error").AsStr())
	default:
		plog.Error("invalid evnet=", event)
	}

}

func main() {

	flag.Parse()
	defer plog.Flush()

	if runtime.GOOS == "darwin" {
		networkmgr = NewDarwinNetworkManager()
	} else if runtime.GOOS == "linux" {
		networkmgr = NewLinuxNetworkManager()
	} else {
		plog.Fatal("os platform not support")
	}

	var err error

	device, err = core.NewTunDevice()

	if err != nil {
		plog.Fatal("create device fail,", err)
	}

	client, err := core.NewPoleVpnClient()

	if err != nil {
		plog.Fatal("new polevpn client fail,", err)
	}

	client.SetEventHandler(eventHandler)
	client.AttachTunDevice(device)

	err = client.Start(endpoint, user, pwd, sni, !verifySSL)
	if err != nil {
		plog.Fatal("start polevpn client fail,", err)
	}

	signalHandler(client)

	client.WaitStop()
}
