package main

import (
	"errors"
	"io"
	"os/exec"

	"github.com/polevpn/elog"
	"github.com/polevpn/netstack/tcpip/header"
	"github.com/polevpn/netstack/tcpip/transport/icmp"
	"github.com/polevpn/netstack/tcpip/transport/tcp"
	"github.com/polevpn/netstack/tcpip/transport/udp"
	"github.com/songgao/water"
	"golang.org/x/net/dns/dnsmessage"
)

type TunIO struct {
	ifce      *water.Interface
	wch       chan []byte
	mtu       int
	wsconn    *WebSocketConn
	forwarder *LocalForwarder
	closed    bool
}

func NewTunIO(size int) (*TunIO, error) {

	config := water.Config{
		DeviceType: water.TUN,
	}
	ifce, err := water.New(config)
	if err != nil {
		return nil, err
	}

	return &TunIO{ifce: ifce, wch: make(chan []byte, size), mtu: 1500, closed: false}, nil
}

func (t *TunIO) SetWebSocketConn(wsconn *WebSocketConn) {
	t.wsconn = wsconn
}

func (t *TunIO) SetLocalForwarder(forwarder *LocalForwarder) {
	t.forwarder = forwarder
}

func (t *TunIO) SetIPAddressAndEnable(ip1 string, ip2 string) error {

	out, err := exec.Command("bash", "-c", "ifconfig "+t.ifce.Name()+" "+ip1+" "+ip2+" up").Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return nil
}

func (t *TunIO) AddRoute(cidr string, gw string) error {

	out, err := exec.Command("bash", "-c", "route -n add -net "+cidr+" "+gw).Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return err

}

func (t *TunIO) Close() error {

	if t.closed == true {
		return nil
	}
	if t.wch != nil {
		t.wch <- nil
		close(t.wch)
	}
	t.closed = true
	return t.ifce.Close()
}

func (t *TunIO) IsClosed() bool {
	return t.closed
}

func (t *TunIO) StartProcess() {
	go t.read()
	go t.write()
}

func (t *TunIO) read() {

	defer func() {
		t.Close()
	}()

	defer PanicHandler()

	for {
		pkt := make([]byte, t.mtu)
		n, err := t.ifce.Read(pkt)
		if err != nil {
			elog.Error("read pkg from tun fail", err)
			return
		}
		pkt = pkt[:n]
		t.dispatch(pkt)
	}

}

func (t *TunIO) dispatch(pkt []byte) {

	var err error
	ipv4pkg := header.IPv4(pkt)
	if ipv4pkg.Protocol() == uint8(icmp.ProtocolNumber4) {
		icmppkg := header.ICMPv4(pkt[20:])
		elog.Info("icmp packet", icmppkg.Sequence())
	} else if ipv4pkg.Protocol() == uint8(tcp.ProtocolNumber) {
		tcppkg := header.TCP(pkt[20:])
		elog.Info("tcp packet", tcppkg.SourcePort(), tcppkg.DestinationPort())
	} else if ipv4pkg.Protocol() == uint8(udp.ProtocolNumber) {
		udppkg := header.UDP(pkt[20:])
		elog.Info("udp packet", udppkg.SourcePort(), udppkg.DestinationPort())

		if udppkg.DestinationPort() == 53 {
			var msg dnsmessage.Message
			err = msg.Unpack(pkt[28:])
			if err != nil {
				elog.Error("parser dns packet fail", err)
			} else {
				for i := 0; i < len(msg.Questions); i++ {
					elog.Info(msg.Questions[i].Name.String(), msg.Questions[i].Type)
				}
			}
		}

	} else {
		elog.Info("unknown packet")
		return
	}

	if t.wsconn == nil {
		elog.Error("wsconn is nil")
		return
	}

	t.forwarder.Write(pkt)

	// buf := make([]byte, POLE_PACKET_HEADER_LEN+len(pkt))
	// copy(buf[POLE_PACKET_HEADER_LEN:], pkt)
	// polepkt := PolePacket(buf)
	// polepkt.SetCmd(CMD_C2S_IPDATA)
	// t.wsconn.Send(polepkt)

}

func (t *TunIO) write() {
	defer PanicHandler()
	for {
		select {
		case pkt, ok := <-t.wch:
			if !ok {
				elog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					elog.Info("exit write process")
					return
				}
				_, err := t.ifce.Write(pkt)
				if err != nil {
					if err == io.EOF {
						elog.Info("tun may be closed")
					} else {
						elog.Error("tun write error", err)
					}
					return
				}
			}
		}
	}
}

func (t *TunIO) Enqueue(pkt []byte) {

	if t.IsClosed() {
		elog.Error("tun device have been closed")
		return
	}

	if t.wch != nil {
		t.wch <- pkt
	}
}
