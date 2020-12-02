package polevpnmobile

import (
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/polevpn/polevpn_client/core"
)

const (
	HTTP_ERROR_NETWORK = 1000
	HTTP_OK            = 0
)

type ResponseEvent interface {
	OnResponse(ret int, msg string, response string)
}

var apiHost = ""
var client = &http.Client{}

var configEndpoint = []byte{141, 116, 190, 44, 154, 39, 78, 125, 124, 48, 172, 194, 105, 232, 215, 81, 150, 121, 161, 71, 11, 198, 157, 103, 161, 9, 13, 143, 174, 238, 13, 216, 116, 200, 67, 0, 158, 152, 236, 28, 215, 149, 28, 206, 102, 208, 210, 204, 76, 170, 0, 134, 176, 146, 173, 187, 223, 249, 244, 166, 162, 77, 5, 17}

func GetSystemConfig(response ResponseEvent) {

	originConfigEndpoint, err := core.AesDecrypt(configEndpoint, core.AesKey)

	if err != nil {
		response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
		return
	}

	resp, err := http.Get(string(originConfigEndpoint))
	if err != nil {
		response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
		return
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
		return
	}

	encrypted, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
		return
	}
	origin, err := core.AesDecrypt(encrypted, core.AesKey)
	if err != nil {
		response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
		return
	}
	response.OnResponse(HTTP_OK, "", string(origin))
}

func Api(host string, api string, header string, reqBody string, response ResponseEvent) {

	go func() {

		encrypted, _ := base64.StdEncoding.DecodeString(host)
		origin, _ := core.AesDecrypt(encrypted, core.AesKey)

		if origin == nil {
			response.OnResponse(HTTP_ERROR_NETWORK, "invalid host", "")
			return
		}

		request, err := http.NewRequest("POST", string(origin)+api, strings.NewReader(reqBody))

		if err != nil {
			response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
			return
		}
		var headers = map[string]string{}
		json.Unmarshal([]byte(header), &headers)

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

		if resp.Header.Get("X-Encrypted") == "true" {
			origin, err := core.AesDecrypt(data, core.AesKey)
			if err != nil {
				response.OnResponse(HTTP_ERROR_NETWORK, err.Error(), "")
				return
			}
			response.OnResponse(HTTP_OK, "ok", string(origin))
		} else {
			response.OnResponse(HTTP_OK, "ok", string(data))
		}

	}()
}
