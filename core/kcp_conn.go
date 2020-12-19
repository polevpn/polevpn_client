package core

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/kcp-go/v5"
)

const (
	CH_KCP_WRITE_SIZE     = 100
	KCP_HANDSHAKE_TIMEOUT = 5
	KCP_MTU               = 1350
	KCP_RECV_WINDOW       = 512
	KCP_SEND_WINDOW       = 128
	KCP_READ_BUFFER       = 4194304
	KCP_WRITE_BUFFER      = 4194304
)

var KCP_KEY = []byte{0x17, 0xef, 0xad, 0x3b, 0x12, 0xed, 0xfa, 0xc9, 0xd7, 0x54, 0x14, 0x5b, 0x3a, 0x4f, 0xb5, 0xf6}

type KCPConn struct {
	conn    *kcp.UDPSession
	wch     chan []byte
	closed  bool
	handler map[uint16]func(PolePacket, Conn)
	mutex   *sync.Mutex
	localip string
}

func NewKCPConn() *KCPConn {
	return &KCPConn{
		conn:    nil,
		closed:  true,
		wch:     nil,
		handler: make(map[uint16]func(PolePacket, Conn)),
		mutex:   &sync.Mutex{},
	}
}

func (kc *KCPConn) SetLocalIP(ip string) {
	kc.localip = ip
}

func (kc *KCPConn) getAuthRequest(user string, pwd string, ip string) (PolePacket, error) {
	av := anyvalue.New()
	av.Set("user", user).Set("pwd", pwd).Set("ip", ip)
	body, err := av.EncodeJson()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, POLE_PACKET_HEADER_LEN+len(body))
	copy(buf[POLE_PACKET_HEADER_LEN:], body)
	pkt := PolePacket(buf)
	pkt.SetCmd(CMD_USER_AUTH)
	pkt.SetLen(uint16(len(buf)))
	return pkt, nil
}

func (kc *KCPConn) readAuthResponse(conn *kcp.UDPSession) (PolePacket, error) {
	var preOffset = 0
	prefetch := make([]byte, 2)
	for {
		n, err := conn.Read(prefetch[preOffset:])
		if err != nil {
			return nil, err
		}
		preOffset += n
		if preOffset >= 2 {
			break
		}
	}
	length := binary.BigEndian.Uint16(prefetch)
	if length < POLE_PACKET_HEADER_LEN {
		return nil, errors.New("invalid pkt len")
	}
	pkt := make([]byte, length)
	copy(pkt, prefetch)
	var offset uint16 = 2
	for {
		n, err := conn.Read(pkt[offset:])
		if err != nil {
			return nil, err
		}
		offset += uint16(n)
		if offset >= length {
			break
		}
	}

	ppkt := PolePacket(pkt)

	return ppkt, nil
}

func (kc *KCPConn) Connect(endpoint string, user string, pwd string, ip string) error {

	localip := kc.localip
	var laddr *net.UDPAddr
	if localip != "" {
		laddr, _ = net.ResolveUDPAddr("udp4", localip+":0")
	}
	block, _ := kcp.NewAESBlockCrypt(KCP_KEY)
	conn, err := kcp.DialWithOptions(endpoint, laddr, block, 10, 3)

	if err != nil {
		return err
	}
	conn.SetMtu(KCP_MTU)
	conn.SetACKNoDelay(true)
	conn.SetStreamMode(true)
	conn.SetNoDelay(1, 10, 2, 1)
	conn.SetWindowSize(KCP_SEND_WINDOW, KCP_RECV_WINDOW)
	conn.SetReadBuffer(KCP_READ_BUFFER)
	conn.SetReadBuffer(KCP_WRITE_BUFFER)
	conn.SetReadDeadline(time.Now().Add(time.Second * KCP_HANDSHAKE_TIMEOUT))

	pkt, err := kc.getAuthRequest(user, pwd, ip)
	if err != nil {
		return err
	}
	_, err = conn.Write([]byte(pkt))

	rpkt, err := kc.readAuthResponse(conn)

	if err != nil {
		return err
	}

	av, err := anyvalue.NewFromJson(rpkt.Payload())

	if err != nil {
		return err
	}

	ret := av.Get("ret").AsInt()

	if ret != 0 {
		if ret == 400 {
			return ErrIPNotExist
		} else if ret == 403 {
			return ErrLoginVerify
		} else {
			return ErrConnectUnknown
		}
	}

	conn.SetReadDeadline(time.Time{})

	kc.mutex.Lock()
	defer kc.mutex.Unlock()

	kc.conn = conn
	kc.wch = make(chan []byte, CH_KCP_WRITE_SIZE)
	kc.closed = false
	return nil
}

func (kc *KCPConn) Close(flag bool) error {
	kc.mutex.Lock()
	defer kc.mutex.Unlock()

	if kc.closed == false {
		kc.closed = true
		if kc.wch != nil {
			kc.wch <- nil
			close(kc.wch)
		}
		err := kc.conn.Close()
		if flag {
			pkt := make([]byte, POLE_PACKET_HEADER_LEN)
			PolePacket(pkt).SetCmd(CMD_CLIENT_CLOSED)
			go kc.dispatch(pkt)
		}
		return err
	}
	return nil
}

func (kc *KCPConn) String() string {
	return kc.conn.LocalAddr().String() + "->" + kc.conn.RemoteAddr().String()
}

func (kc *KCPConn) IsClosed() bool {
	kc.mutex.Lock()
	defer kc.mutex.Unlock()

	return kc.closed
}

func (kc *KCPConn) SetHandler(cmd uint16, handler func(PolePacket, Conn)) {
	kc.handler[cmd] = handler
}

func (kc *KCPConn) read() {
	defer func() {
		kc.Close(true)
	}()

	defer PanicHandler()

	for {
		var preOffset = 0

		prefetch := make([]byte, 2)

		for {
			n, err := kc.conn.Read(prefetch[preOffset:])
			if err != nil {
				if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
					plog.Info(kc.String(), "conn closed")
				} else {
					plog.Error(kc.String(), "conn read exception:", err)
				}
				return
			}
			preOffset += n
			if preOffset >= 2 {
				break
			}
		}

		len := binary.BigEndian.Uint16(prefetch)

		if len < POLE_PACKET_HEADER_LEN {
			plog.Error("invalid packet len")
			continue
		}

		pkt := make([]byte, len)
		copy(pkt, prefetch)
		var offset uint16 = 2
		for {
			n, err := kc.conn.Read(pkt[offset:])
			if err != nil {
				if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
					plog.Info(kc.String(), "conn closed")
				} else {
					plog.Error(kc.String(), "conn read exception:", err)
				}
				return
			}
			offset += uint16(n)
			if offset >= len {
				break
			}
		}

		kc.dispatch(pkt)

	}

}

func (kc *KCPConn) dispatch(pkt []byte) {
	ppkt := PolePacket(pkt)

	handler, ok := kc.handler[ppkt.Cmd()]
	if ok {
		handler(pkt, kc)
	} else {
		plog.Error("invalid pkt cmd=", ppkt.Cmd())
	}
}

func (kc *KCPConn) write() {
	defer PanicHandler()

	for {
		select {
		case pkt, ok := <-kc.wch:
			if !ok {
				plog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					plog.Info("exit write process")
					return
				}
				_, err := kc.conn.Write(pkt)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						plog.Info(kc.String(), "conn closed")
					} else {
						plog.Error(kc.String(), "conn write exception:", err)
					}
					return
				}
			}
		}
	}
}

func (kc *KCPConn) Send(pkt []byte) {
	if kc.IsClosed() == true {
		plog.Debug("websocket connection is closed,can't send pkt")
		return
	}
	if kc.wch != nil {
		kc.wch <- pkt
	}
}

func (kc *KCPConn) StartProcess() {
	go kc.read()
	go kc.write()
}
