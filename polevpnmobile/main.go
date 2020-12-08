package polevpnmobile

import (
	"encoding/base64"
	"os"
	"sync"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/polevpn_client/core"
)

const (
	POLEVPN_MOBILE_INIT     = 0
	POLEVPN_MOBILE_STARTED  = 1
	POLEVPN_MOBILE_STOPPED  = 2
	POLEVPN_MOBILE_STARTING = 3
	POLEVPN_MOBILE_STOPPING = 4
)

var plog *elog.EasyLogger

type PoleVPNEventHandler interface {
	OnStartedEvent()
	OnStoppedEvent()
	OnErrorEvent(errtype string, errmsg string)
	OnAllocEvent(ip string, dns string)
	OnReconnectingEvent()
	OnReconnectedEvent()
}

type PoleVPNLogHandler interface {
	OnWrite(data string)
	OnFlush()
}

type PoleVPN struct {
	handler PoleVPNEventHandler
	client  *core.PoleVpnClient
	mutex   *sync.Mutex
	mode    bool
	localip string
	state   int
}

type logHandler struct {
}

var externLogHandler PoleVPNLogHandler

func (lh *logHandler) Write(data []byte) (int, error) {

	if externLogHandler != nil {
		externLogHandler.OnWrite(string(data))
		return len(data), nil
	} else {
		return os.Stderr.Write(data)
	}
}

func (lh *logHandler) Flush() {

	if externLogHandler != nil {
		externLogHandler.OnFlush()
	} else {
		os.Stderr.Sync()
	}
}

func init() {

	plog = elog.NewEasyLogger("INFO", false, 1, &logHandler{})
	core.SetLogger(plog)
	defer plog.Flush()
}

func SetLogLevel(level string) {
	plog.SetLogLevel(level)
}

func SetLogHandler(handler PoleVPNLogHandler) {
	externLogHandler = handler
}

func NewPoleVPN() *PoleVPN {
	return &PoleVPN{mutex: &sync.Mutex{}, state: POLEVPN_MOBILE_INIT}
}

func (pvm *PoleVPN) eventHandler(event int, client *core.PoleVpnClient, av *anyvalue.AnyValue) {

	switch event {
	case core.CLIENT_EVENT_ADDRESS_ALLOCED:
		{
			if pvm.handler != nil {
				pvm.handler.OnAllocEvent(av.Get("ip").AsStr(), av.Get("dns").AsStr())
			}
		}
	case core.CLIENT_EVENT_STOPPED:
		{
			pvm.state = POLEVPN_MOBILE_STOPPED
			if pvm.handler != nil {
				pvm.handler.OnStoppedEvent()
			}
		}
	case core.CLIENT_EVENT_RECONNECTED:
		if pvm.handler != nil {
			pvm.handler.OnReconnectedEvent()
		}
	case core.CLIENT_EVENT_RECONNECTING:
		if pvm.handler != nil {
			pvm.handler.OnReconnectingEvent()
		}
	case core.CLIENT_EVENT_STARTED:
		pvm.state = POLEVPN_MOBILE_STARTED
		if pvm.handler != nil {
			pvm.handler.OnStartedEvent()
		}
	case core.CLIENT_EVENT_ERROR:
		if pvm.handler != nil {
			pvm.handler.OnErrorEvent(av.Get("type").AsStr(), av.Get("error").AsStr())
		}
	default:
		plog.Error("invalid evnet=", event)
	}

}

func (pvm *PoleVPN) Attach(fd int) {
	if pvm.state != POLEVPN_MOBILE_STARTED {
		return
	}
	tundevice := core.NewTunDevice()
	tundevice.Attach(fd)
	pvm.client.AttachTunDevice(tundevice)
}

func (pvm *PoleVPN) Start(endpoint string, user string, pwd string, sni string) {

	pvm.mutex.Lock()
	defer pvm.mutex.Unlock()
	if pvm.state != POLEVPN_MOBILE_INIT && pvm.state != POLEVPN_MOBILE_STOPPED {
		return
	}

	encrypted, _ := base64.StdEncoding.DecodeString(endpoint)
	originEndpiont, _ := core.AesDecrypt(encrypted, core.AesKey)

	if originEndpiont == nil {
		if pvm.handler != nil {
			pvm.handler.OnErrorEvent("start", "invalid endpoint")
		}
		return
	}

	client, err := core.NewPoleVpnClient()
	if err != nil {
		if pvm.handler != nil {
			pvm.handler.OnErrorEvent("start", err.Error())
		}
		return
	}
	client.SetLocalIP(pvm.localip)
	client.SetRouteMode(pvm.mode)

	pvm.client = client
	pvm.state = POLEVPN_MOBILE_STARTING
	pvm.client.SetEventHandler(pvm.eventHandler)
	go pvm.client.Start(string(originEndpiont), user, pwd, sni)
}

func (pvm *PoleVPN) Stop() {
	pvm.mutex.Lock()
	defer pvm.mutex.Unlock()
	if pvm.state == POLEVPN_MOBILE_STARTED || pvm.state == POLEVPN_MOBILE_STARTING {
		pvm.state = POLEVPN_MOBILE_STOPPING
		go pvm.client.Stop()
	}

}

func (pvm *PoleVPN) SetLocalIP(ip string) {
	if pvm.state == POLEVPN_MOBILE_STARTED || pvm.state == POLEVPN_MOBILE_STARTING {
		pvm.client.SetLocalIP(ip)
	}
	pvm.localip = ip
}

func (pvm *PoleVPN) SetRouteMode(mode bool) {
	if pvm.state == POLEVPN_MOBILE_STARTED || pvm.state == POLEVPN_MOBILE_STARTING {
		pvm.client.SetRouteMode(mode)
	}
	pvm.mode = mode
}

func (pvm *PoleVPN) GetState() int {
	return pvm.state
}

func (pvm *PoleVPN) CloseConnect(flag bool) {

	if pvm.state == POLEVPN_MOBILE_STARTED {
		pvm.client.CloseConnect(flag)
	}

}

func (pvm *PoleVPN) SetEventHandler(handler PoleVPNEventHandler) {
	pvm.handler = handler
}
