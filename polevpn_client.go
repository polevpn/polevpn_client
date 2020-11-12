package main

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
)

const (
	POLE_CLIENT_INIT        = 0
	POLE_CLIENT_RUNING      = 1
	POLE_CLIENT_CLOSED      = 2
	POLE_CLIENT_RECONNETING = 3
)

const (
	CH_TUN_DEVICE_WRITE_SIZE     = 256
	HEART_BEAT_INTERVAL          = 10
	WEBSOCKET_RECONNECT_TIMES    = 12
	WEBSOCKET_RECONNECT_INTERVAL = 10
	WEBSOCKET_NO_HEARTBEAT_TIMES = 4
)

type PoleVpnClient struct {
	tunio             *TunIO
	wsconn            *WebSocketConn
	forwarder         *LocalForwarder
	state             int
	mutex             *sync.Mutex
	endpoint          string
	user              string
	pwd               string
	forwardcidr       string
	allocip           string
	lasttimeHeartbeat time.Time
	reconnecting      bool
}

func NewPoleVpnClient() (*PoleVpnClient, error) {

	var err error

	tunio, err := NewTunIO(CH_TUN_DEVICE_WRITE_SIZE)

	if err != nil {
		elog.Fatal("create tun fail", err)
		return nil, err
	}

	wsconn := NewWebSocketConn()

	forwarder, err := NewLocalForwarder()

	if err != nil {
		elog.Error("websocket connect fail", err)
		return nil, err
	}

	client := &PoleVpnClient{tunio: tunio, wsconn: wsconn, forwarder: forwarder, state: POLE_CLIENT_INIT, mutex: &sync.Mutex{}}

	return client, nil
}

func (pc *PoleVpnClient) Start(endpoint string, user string, pwd string, forwardcidr string) error {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state != POLE_CLIENT_INIT {
		return errors.New("client stoped or not init")
	}

	pc.endpoint = endpoint
	pc.user = user
	pc.pwd = pwd
	pc.forwardcidr = forwardcidr

	var err error

	err = pc.wsconn.Connect(endpoint, user, pwd)
	if err != nil {
		elog.Fatal("websocket connect fail", err)
		return err
	}

	pc.wsconn.SetHandler(CMD_ALLOC_IPADDR, pc.handlerAllocAdressRespose)

	pc.wsconn.SetHandler(CMD_S2C_IPDATA, pc.handlerIPDataResponse)

	pc.wsconn.SetHandler(CMD_CLIENT_CLOSED, pc.handlerWSConnCloseEvent)
	pc.wsconn.SetHandler(CMD_HEART_BEAT, pc.handlerHeartBeatRespose)

	pc.forwarder.SetTunIO(pc.tunio)
	pc.tunio.SetWebSocketConn(pc.wsconn)
	pc.tunio.SetLocalForwarder(pc.forwarder)

	pc.wsconn.StartProcess()
	pc.forwarder.StartProcess()
	pc.tunio.StartProcess()
	pc.AskAllocIPAddress()
	pc.lasttimeHeartbeat = time.Now()
	go pc.HeartBeat()
	pc.state = POLE_CLIENT_RUNING
	return nil
}

func (pc *PoleVpnClient) AskAllocIPAddress() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_ALLOC_IPADDR)
	pc.wsconn.Send(buf)
}

func (pc *PoleVpnClient) handlerAllocAdressRespose(pkt PolePacket, wsc *WebSocketConn) {

	av, err := anyvalue.NewFromJson(pkt.Payload())

	if err != nil {
		elog.Error("alloc address av decode fail", err)
		pc.Stop()
		return
	}

	ip1 := av.Get("ip").AsStr()

	if ip1 == "" {
		elog.Error("alloc ip fail,stop client")
		pc.Stop()
		return
	}

	ip := net.ParseIP(ip1).To4()
	ip2 := net.IPv4(ip[0], ip[1], ip[2], ip[3]-1).To4().String()

	elog.Infof("set ip src ip:%v,dst ip:%v", ip1, ip2)
	err = pc.tunio.SetIPAddressAndEnable(ip1, ip2)
	if err != nil {
		elog.Error("set address fail", err)
		pc.Stop()
		return
	}

	elog.Infof("set route %v to %v", pc.forwardcidr, ip2)
	err = pc.tunio.AddRoute(pc.forwardcidr, ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.allocip = ip1

}

func (pc *PoleVpnClient) handlerHeartBeatRespose(pkt PolePacket, wsc *WebSocketConn) {
	elog.Info("received heartbeat")
	pc.lasttimeHeartbeat = time.Now()
}

func (pc *PoleVpnClient) handlerIPDataResponse(pkt PolePacket, wsc *WebSocketConn) {
	pc.tunio.Enqueue(pkt[POLE_PACKET_HEADER_LEN:])
}

func (pc *PoleVpnClient) handlerWSConnCloseEvent(pkt PolePacket, wsc *WebSocketConn) {
	elog.Info("ws client closed,start reconnect")
	pc.reconnect()
}

func (pc *PoleVpnClient) reconnect() {

	if pc.reconnecting == true {
		elog.Info("wsconn is reconnecting")
		return
	}

	pc.reconnecting = true
	pc.state = POLE_CLIENT_RECONNETING

	success := false
	for i := 0; i < WEBSOCKET_RECONNECT_TIMES; i++ {

		elog.Info("reconnecting")
		err := pc.wsconn.ReConnect(pc.endpoint, pc.user, pc.pwd, pc.allocip)
		if err != nil {
			elog.Error("reconnect fail", err)
			elog.Error("reconnect 10 seconds later")
			time.Sleep(time.Second * WEBSOCKET_RECONNECT_INTERVAL)
			continue
		} else {
			elog.Info("reconnect ok")
			pc.wsconn.StartProcess()
			success = true
			pc.state = POLE_CLIENT_RUNING
			break
		}
	}
	if success == false {
		elog.Error("reconnet failed,stop client")
		pc.Stop()
	}
	pc.reconnecting = false
}

func (pc *PoleVpnClient) HeartBeat() {

	timer := time.NewTicker(time.Second * time.Duration(HEART_BEAT_INTERVAL))

	for _ = range timer.C {
		if pc.state == POLE_CLIENT_CLOSED {
			timer.Stop()
			break
		}
		timeNow := time.Now()
		if timeNow.Sub(pc.lasttimeHeartbeat) > time.Second*HEART_BEAT_INTERVAL*WEBSOCKET_NO_HEARTBEAT_TIMES {
			elog.Error("have not recevied heartbeat for", WEBSOCKET_NO_HEARTBEAT_TIMES, "times,close connection and reconnet")
			pc.wsconn.Close(true)
			pc.lasttimeHeartbeat = timeNow
			continue
		}
		buf := make([]byte, POLE_PACKET_HEADER_LEN)
		PolePacket(buf).SetCmd(CMD_HEART_BEAT)
		pc.wsconn.Send(buf)
	}

}

func (pc *PoleVpnClient) Stop() {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state == POLE_CLIENT_CLOSED {
		elog.Error("client have been closed")
		return
	}

	elog.Error("client closing")
	pc.tunio.Close()
	pc.wsconn.Close(false)
	pc.forwarder.Close()
	pc.state = POLE_CLIENT_CLOSED
	elog.Error("client closed")

}
