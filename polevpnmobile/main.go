package polevpnmobile

import (
	"os"
	"sync"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/poleclient/core"
)

const (
	POLEVPN_MOBILE_INIT     = 0
	POLEVPN_MOBILE_STARTED  = 1
	POLEVPN_MOBILE_STOPPED  = 2
	POLEVPN_MOBILE_STARTING = 3
	POLEVPN_MOBILE_STOPPING = 4
)

type PoleVPNEventHandler interface {
	OnStartedEvent()
	OnStoppedEvent()
	OnErrorEvent(errtype string, errmsg string)
	OnAllocEvent(ip string, dns string)
	OnReconnectingEvent()
	OnReconnectedEvent()
}

type PoleVPN struct {
	handler PoleVPNEventHandler
	client  *core.PoleVpnClient
	mutex   *sync.Mutex
	mode    bool
	localip string
	state   int
}

var plog *elog.EasyLogger

type logHandler struct {
}

func (lh *logHandler) Write(data []byte) (int, error) {

	return os.Stderr.Write(data)
}

func (lh *logHandler) Flush() {
	os.Stderr.Sync()
}

func init() {

	plog = elog.NewEasyLogger("INFO", false, 1, &logHandler{})
	core.SetLogger(plog)
	defer plog.Flush()
}

func SetLogLevel(level string) {
	plog.SetLogLevel(level)
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
			if pvm.handler != nil {
				pvm.handler.OnStoppedEvent()
			}
			pvm.state = POLEVPN_MOBILE_STOPPED
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
		if pvm.handler != nil {
			pvm.handler.OnStartedEvent()
		}
		pvm.state = POLEVPN_MOBILE_STARTED
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
	go pvm.client.Start(endpoint, user, pwd, sni)
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

func (pvm *PoleVPN) CloseConnect(flag bool) {

	if pvm.state == POLEVPN_MOBILE_STARTED {
		pvm.client.CloseConnect(flag)
	}

}

func (pvm *PoleVPN) SetEventHandler(handler PoleVPNEventHandler) {
	pvm.handler = handler
}
