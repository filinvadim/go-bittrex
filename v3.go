package gobittrex

import (
	"bytes"
	"compress/flate"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	HostV3 = "socket-v3.bittrex.com"
	HubV3  = "c3"
	schema = "https"
)

type ClientV3 struct {
	cli *Client

	logln func(...interface{})
}

func NewClientV3(logf func(...interface{})) (*ClientV3, error) {
	client := NewWebsocketClient(logf)
	client.OnMessageError = func(err error) { logf(err) }
	err := client.Connect(schema, HostV3, []string{HubV3})
	if err != nil {
		return nil, err
	}
	return &ClientV3{client, logf}, nil
}

func (c *ClientV3) SetErrorHandler(h func(err error)) {
	c.cli.OnMessageError = h
}

func (c *ClientV3) SetResponseHandler(h func(hub, method string, payload []byte)) {
	c.cli.OnClientMethod = func(hub, method string, arguments []json.RawMessage) {
		for _, a := range arguments {
			unq, err := strconv.Unquote(string(a))
			if err != nil {
				c.cli.OnMessageError(err)
				return
			}
			bt, err := base64.StdEncoding.DecodeString(unq)
			if err != nil {
				c.cli.OnMessageError(err)
				return
			}
			decompressed, err := decompress(bt)
			if err != nil {
				c.cli.OnMessageError(err)
				return
			}
			h(hub, method, decompressed)
		}
	}
}

func decompress(input []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(input))
	defer reader.Close()

	buf := bytes.NewBuffer(nil)
	_, err := io.Copy(buf, reader)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type Response struct {
	Success   bool    `json:"Success"`
	ErrorCode *string `json:"ErrorCode"`
}

func (c *ClientV3) Authenticate(key, secret string) error {
	ts := time.Now().UTC().Unix() * 1000
	randomId := uuid.New()
	randomContent := fmt.Sprintf("%d%s", ts, randomId)

	resp, err := c.cli.CallHub(
		HubV3,
		"Authenticate",
		key,
		ts,
		randomId,
		sign(randomContent, secret),
	)
	if err != nil {
		return err
	}

	var authResp Response
	if err = json.Unmarshal(resp, &authResp); err != nil {
		return err
	}
	if !authResp.Success {
		errMsg := "auth unsuccessful"
		if authResp.ErrorCode != nil {
			errMsg = *authResp.ErrorCode
		}
		return errors.New(errMsg)
	}
	return nil
}

func sign(data, secret string) string {
	hmacWriter := hmac.New(sha512.New, []byte(secret))
	if _, err := io.WriteString(hmacWriter, data); err != nil {
		return ""
	}
	signature := hex.EncodeToString(hmacWriter.Sum(nil))
	return strings.ToUpper(signature)
}

func (c *ClientV3) IsAuthDone() bool {
	resp, err := c.cli.CallHub(HubV3, "IsAuthenticated")
	if err != nil {
		c.logln(err)
		return false
	}
	return string(resp) == "true"
}

func (c *ClientV3) Subscribe(channels ...string) error {
	resp, err := c.cli.CallHub(HubV3, "Subscribe", channels)
	if err != nil {
		return err
	}

	var subResponses []Response
	if err = json.Unmarshal(resp, &subResponses); err != nil {
		return err
	}

	for _, resp := range subResponses {
		if !resp.Success {
			errMsg := fmt.Sprintf("subscription failed")
			if resp.ErrorCode != nil {
				errMsg = *resp.ErrorCode
			}
			return errors.New(errMsg)
		}
	}

	return err
}

func (c *ClientV3) Unsubscribe(channels ...string) error {
	resp, err := c.cli.CallHub(HubV3, "Unsubscribe", channels)
	if err != nil {
		return err
	}

	var unSubResponses []Response
	if err = json.Unmarshal(resp, &unSubResponses); err != nil {
		return err
	}

	for _, resp := range unSubResponses {
		if !resp.Success {
			errMsg := fmt.Sprintf("unsubscription failed")
			if resp.ErrorCode != nil {
				errMsg = *resp.ErrorCode
			}
			return errors.New(errMsg)
		}
	}

	return err
}

func (c *ClientV3) Close() error {
	return c.cli.Close()
}
