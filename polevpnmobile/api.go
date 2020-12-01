package polevpnmobile

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
)

const (
	HTTP_ERROR_NETWORK = 1000
	HTTP_OK            = 0
)

type ResponseEvent interface {
	OnResponse(ret int, msg string, response string)
}

var ApiHost = "http://hrtv.freemyip.com:8080"
var client = &http.Client{}

func Api(api string, header string, reqBody string, response ResponseEvent) {

	go func() {

		request, err := http.NewRequest("POST", ApiHost+api, strings.NewReader(reqBody))

		if err != nil {
			response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
			return
		}
		var headers = map[string]string{}
		json.Unmarshal([]byte(header), &headers)

		plog.Infof("headers %v", headers)

		for k, v := range headers {
			request.Header.Add(k, v)
		}
		request.Header.Add("Content-Type", "application/json")

		resp, err := client.Do(request)

		if err != nil {
			response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ret, _ := strconv.Atoi(resp.Header.Get("X-Error-Ret"))
			msg := resp.Header.Get("X-Error-Msg")
			if ret == 0 {
				ret = resp.StatusCode
				msg = resp.Status
				return
			}
			response.OnResponse(ret, msg, "")
			return
		}

		data, err := ioutil.ReadAll(resp.Body)

		if err != nil {
			response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
			return
		}

		response.OnResponse(HTTP_OK, "ok", string(data))

	}()
}
