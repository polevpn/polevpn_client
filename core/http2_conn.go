package core

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/polevpn/h2conn"
	"github.com/polevpn/xnet/http2"
)

const (
	CH_HTTP2_WRITE_SIZE     = 100
	HTTP2_HANDSHAKE_TIMEOUT = 5
)

type Http2Conn struct {
	conn    *h2conn.Conn
	wch     chan []byte
	closed  bool
	handler map[uint16]func(PolePacket, Conn)
	mutex   *sync.Mutex
	localip string
}

func NewHttp2Conn() *Http2Conn {
	return &Http2Conn{
		conn:    nil,
		closed:  true,
		wch:     nil,
		handler: make(map[uint16]func(PolePacket, Conn)),
		mutex:   &sync.Mutex{},
	}
}

func (h2c *Http2Conn) SetLocalIP(ip string) {
	h2c.localip = ip
}

func (h2c *Http2Conn) Connect(endpoint string, user string, pwd string, ip string) error {

	localip := h2c.localip
	var err error
	var laddr *net.TCPAddr
	if localip != "" {
		laddr, _ = net.ResolveTCPAddr("tcp4", localip+":0")
	}

	dailer := net.Dialer{Timeout: time.Second * HTTP2_HANDSHAKE_TIMEOUT, LocalAddr: laddr}
	var d *h2conn.Client
	if strings.HasPrefix(endpoint, "https://") {
		d = &h2conn.Client{
			Client: &http.Client{
				Transport: &http2.Transport{
					DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
						return tls.DialWithDialer(&dailer, network, addr, cfg)
					},
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			},
		}
	} else {
		d = &h2conn.Client{
			Client: &http.Client{
				Transport: &http2.Transport{
					AllowHTTP: true,
					DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
						return dailer.Dial(network, addr)
					},
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			},
		}
	}

	conn, resp, err := d.Connect(context.Background(), endpoint+"?user="+url.QueryEscape(user)+"&pwd="+url.QueryEscape(pwd)+"&ip="+ip)

	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusBadRequest {
				return ErrIPNotExist
			} else if resp.StatusCode == http.StatusForbidden {
				return ErrLoginVerify
			} else {
				return ErrConnectUnknown
			}
		}
		plog.Error("http2 connect fail,", err)
		return ErrNetwork
	}

	h2c.mutex.Lock()
	defer h2c.mutex.Unlock()

	h2c.conn = conn
	h2c.wch = make(chan []byte, CH_HTTP2_WRITE_SIZE)
	h2c.closed = false
	return nil
}

func (h2c *Http2Conn) Close(flag bool) error {
	h2c.mutex.Lock()
	defer h2c.mutex.Unlock()

	if h2c.closed == false {
		h2c.closed = true
		if h2c.wch != nil {
			h2c.wch <- nil
			close(h2c.wch)
		}
		err := h2c.conn.Close()
		if flag {
			pkt := make([]byte, POLE_PACKET_HEADER_LEN)
			PolePacket(pkt).SetCmd(CMD_CLIENT_CLOSED)
			go h2c.dispatch(pkt)
		}
		return err
	}
	return nil
}

func (h2c *Http2Conn) String() string {
	return h2c.conn.LocalAddr().String() + "->" + h2c.conn.RemoteAddr().String()
}

func (h2c *Http2Conn) IsClosed() bool {
	h2c.mutex.Lock()
	defer h2c.mutex.Unlock()

	return h2c.closed
}

func (h2c *Http2Conn) SetHandler(cmd uint16, handler func(PolePacket, Conn)) {
	h2c.handler[cmd] = handler
}

func (h2c *Http2Conn) read() {
	defer func() {
		h2c.Close(true)
	}()

	defer PanicHandler()

	for {
		var preOffset = 0

		prefetch := make([]byte, 2)

		for {
			n, err := h2c.conn.Read(prefetch[preOffset:])
			if err != nil {
				if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
					plog.Info(h2c.String(), "conn closed")
				} else {
					plog.Error(h2c.String(), "conn read exception:", err)
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
			n, err := h2c.conn.Read(pkt[offset:])
			if err != nil {
				if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
					plog.Info(h2c.String(), "conn closed")
				} else {
					plog.Error(h2c.String(), "conn read exception:", err)
				}
				return
			}
			offset += uint16(n)
			if offset >= len {
				break
			}
		}

		h2c.dispatch(pkt)

	}

}

func (h2c *Http2Conn) dispatch(pkt []byte) {
	ppkt := PolePacket(pkt)

	handler, ok := h2c.handler[ppkt.Cmd()]
	if ok {
		handler(pkt, h2c)
	} else {
		plog.Error("invalid pkt cmd=", ppkt.Cmd())
	}
}

func (h2c *Http2Conn) write() {
	defer PanicHandler()

	for {
		select {
		case pkt, ok := <-h2c.wch:
			if !ok {
				plog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					plog.Info("exit write process")
					return
				}
				_, err := h2c.conn.Write(pkt)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						plog.Info(h2c.String(), "conn closed")
					} else {
						plog.Error(h2c.String(), "conn write exception:", err)
					}
					return
				}
			}
		}
	}
}

func (h2c *Http2Conn) Send(pkt []byte) {
	if h2c.IsClosed() == true {
		plog.Debug("websocket connection is closed,can't send pkt")
		return
	}
	if h2c.wch != nil {
		h2c.wch <- pkt
	}
}

func (h2c *Http2Conn) StartProcess() {
	go h2c.read()
	go h2c.write()
}
