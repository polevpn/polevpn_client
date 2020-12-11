package main

import (
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/polevpn_client/core"
)

var endpoint string
var user string
var pwd string
var sni string
var mode bool

func init() {
	flag.StringVar(&endpoint, "e", "", "server connect to")
	flag.StringVar(&user, "u", "", "login user")
	flag.StringVar(&pwd, "p", "", "login password")
	flag.StringVar(&sni, "s", "www.apple.com", "fake domain")
	flag.BoolVar(&mode, "m", false, "global route mode")
}

func signalHandler(pc *core.PoleVpnClient) {

	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range c {
			switch s {
			case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				elog.Info("receive exit signal,exit")
				if pc != nil {
					pc.Stop()
				}
				elog.Flush()
				os.Exit(0)
			case syscall.SIGUSR1:
			case syscall.SIGUSR2:
			default:
			}
		}
	}()
}

var device *core.TunDevice
var networkmgr core.NetworkManager

func eventHandler(event int, client *core.PoleVpnClient, av *anyvalue.AnyValue) {

	switch event {
	case core.CLIENT_EVENT_ADDRESS_ALLOCED:
		{
			err := networkmgr.SetNetwork(device.GetInterface().Name(), av.Get("ip").AsStr(), av.Get("dns").AsStr())
			if err != nil {
				elog.Error("set network fail,", err)
				client.Stop()
			}
		}
	case core.CLIENT_EVENT_STOPPED:
		{
			elog.Info("client stoped")
			networkmgr.RestoreNetwork()
		}
	case core.CLIENT_EVENT_RECONNECTED:
		elog.Info("client reconnected")
	case core.CLIENT_EVENT_RECONNECTING:
		ip, _ := core.GetLocalIp()
		client.SetLocalIP(ip)
		elog.Info("client reconnecting")
	case core.CLIENT_EVENT_STARTED:
		elog.Info("client started")
	case core.CLIENT_EVENT_ERROR:
		elog.Info("client error", av.Get("error").AsStr())
	default:
		elog.Error("invalid evnet=", event)
	}

}

func main() {

	flag.Parse()
	defer elog.Flush()

	go func() {
		for range time.NewTicker(time.Second * 15).C {
			m := runtime.MemStats{}
			runtime.ReadMemStats(&m)
			elog.Printf("mem=%v,go=%v", m.HeapAlloc, runtime.NumGoroutine())
		}
	}()

	if runtime.GOOS == "darwin" {
		networkmgr = core.NewDarwinNetworkManager()
	} else if runtime.GOOS == "linux" {
		networkmgr = core.NewLinuxNetworkManager()
	} else {
		elog.Fatal("os platform not support")
	}

	device = core.NewTunDevice()
	err := device.Create()

	if err != nil {
		elog.Fatal("create device fail", err)
	}

	ip, err := core.GetLocalIp()

	if err != nil {
		elog.Fatal("get localip fail", err)
	}

	client, err := core.NewPoleVpnClient()

	if err != nil {
		elog.Fatal("new polevpn client fail", err)
	}
	client.SetLocalIP(ip)
	client.SetEventHandler(eventHandler)
	client.SetRouteMode(mode)
	client.AttachTunDevice(device)

	err = client.Start(endpoint, user, pwd)
	if err != nil {
		elog.Fatal("start polevpn client fail", err)
	}

	signalHandler(client)

	client.WaitStop()
}
