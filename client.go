package gobittrex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type negotiationResponse struct {
	Url                     string
	ConnectionToken         string
	ConnectionId            string
	KeepAliveTimeout        float32
	DisconnectTimeout       float32
	ConnectionTimeout       float32
	TryWebSockets           bool
	ProtocolVersion         string
	TransportConnectTimeout float32
	LogPollDelay            float32
}

type Client struct {
	OnMessageError func(err error)
	OnClientMethod func(hub, method string, arguments []json.RawMessage)
	// When client disconnects, the causing error is sent to this channel. Valid only after Connect().
	DisconnectedChannel chan bool
	params              negotiationResponse
	socket              *websocket.Conn
	nextId              int

	Cursor string

	// Futures for server call responses and a guarding mutex.
	responseFutures map[string]chan *serverMessage
	mutex           sync.Mutex
	dispatchRunning bool

	logf func(...interface{})
}

type serverMessage struct {
	Cursor           string            `json:"C,omitempty"`
	Data             []json.RawMessage `json:"M,omitempty"`
	Result           json.RawMessage   `json:"R,omitempty"`
	Identifier       string            `json:"I,omitempty"`
	Error            string            `json:"E,omitempty"`
	Connected        int               `json:"S,omitempty"`
	GroupToken       string            `json:"G,omitempty"`
	ReconnectRequest int               `json:"T,omitempty"`
	ReconnectDelay   int64             `json:"L,omitempty"`
}

func negotiate(scheme, address string) (negotiationResponse, error) {
	var (
		response       negotiationResponse
		negotiationUrl = url.URL{Scheme: scheme, Host: address, Path: "/signalr/negotiate"}
		client         = &http.Client{}
	)

	reply, err := client.Get(negotiationUrl.String())
	if err != nil {
		return response, err
	}

	defer reply.Body.Close()

	body, err := ioutil.ReadAll(reply.Body)
	if err != nil {
		return response, err
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return response, err
	}
	return response, nil
}

func connectWebsocket(address string, params negotiationResponse, hubs []string) (*websocket.Conn, error) {
	var connectionData = make([]struct {
		Name string `json:"Name"`
	}, len(hubs))

	for i, h := range hubs {
		connectionData[i].Name = h
	}

	connectionDataBytes, err := json.Marshal(connectionData)
	if err != nil {
		return nil, err
	}

	var connectionParameters = url.Values{}
	connectionParameters.Set("transport", "webSockets")
	connectionParameters.Set("clientProtocol", "1.5")
	connectionParameters.Set("connectionToken", params.ConnectionToken)
	connectionParameters.Set("connectionData", string(connectionDataBytes))

	var connectionUrl = url.URL{Scheme: "wss", Host: address, Path: "signalr/connect"}
	connectionUrl.RawQuery = connectionParameters.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(connectionUrl.String(), nil)
	return conn, err
}

func (c *Client) routeResponse(response *serverMessage) {
	c.mutex.Lock()

	if msgChan, ok := c.responseFutures[response.Identifier]; ok {
		msgChan <- response
		close(msgChan)
		delete(c.responseFutures, response.Identifier)
	}
	c.mutex.Unlock()
}

func (c *Client) createResponseFuture(identifier string) (chan *serverMessage, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.dispatchRunning {
		return nil, fmt.Errorf("Dispatch is not running")
	}

	var msg = make(chan *serverMessage)
	c.responseFutures[identifier] = msg
	return msg, nil
}

func (c *Client) deleteResponseFuture(identifier string) {
	c.mutex.Lock()
	delete(c.responseFutures, identifier)
	c.mutex.Unlock()
}

func (c *Client) tryStartDispatch() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.dispatchRunning {
		return fmt.Errorf("dispatcher is already running")
	}
	c.DisconnectedChannel = make(chan bool)
	c.dispatchRunning = true

	return nil
}

func (c *Client) endDispatch() {
	// Close all the waiting response futures.
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.dispatchRunning = false
	for _, c := range c.responseFutures {
		close(c)
	}
	c.responseFutures = make(map[string]chan *serverMessage)
	close(c.DisconnectedChannel)
}

func (c *Client) dispatch(connectedChannel chan bool) {
	if err := c.tryStartDispatch(); err != nil {
		c.logf(err)
		return
	}

	defer c.endDispatch()

	for {
		var message serverMessage

		var hubCall struct {
			HubName   string            `json:"H"`
			Method    string            `json:"M"`
			Arguments []json.RawMessage `json:"A"`
		}

		typ, data, err := c.socket.ReadMessage()
		if err != nil {
			if c.OnMessageError != nil {
				c.OnMessageError(err)
			}
			continue
		}
		if typ == websocket.PingMessage {
			c.logf("PING received")
			c.socket.WriteMessage(websocket.PongMessage, []byte{})
			continue
		}

		if err := json.Unmarshal(data, &message); err != nil {
			if c.OnMessageError != nil {
				c.OnMessageError(err)
				continue
			}
		}
		if message.Connected == 1 {
			c.Cursor = message.Cursor
			close(connectedChannel)
			continue
		}

		if len(message.Identifier) > 0 {
			// This is a response to a hub call.
			c.routeResponse(&message)
			continue
		}
		if len(message.Data) == 1 {
			if err := json.Unmarshal(message.Data[0], &hubCall); err == nil && len(hubCall.HubName) > 0 && len(hubCall.Method) > 0 {
				// This is a client Hub method call from server.
				if c.OnClientMethod != nil {
					c.OnClientMethod(hubCall.HubName, hubCall.Method, hubCall.Arguments)
				}
				continue
			}
		}
		if bytes.Contains(data, []byte("{}")) {
			c.logf("keepalive message received")
			continue
		}
		if len(data) != 0 {
			c.logf("unrecognized data: ", string(data))
		}
	}
}

// Call server hub method. Dispatch() function must be running, otherwise this method will never return.
func (c *Client) CallHub(hub, method string, params ...interface{}) (json.RawMessage, error) {
	var request = struct {
		Hub        string        `json:"H"`
		Method     string        `json:"M"`
		Arguments  []interface{} `json:"A"`
		Identifier int           `json:"I"`
	}{
		Hub:        hub,
		Method:     method,
		Arguments:  params,
		Identifier: c.nextId,
	}

	c.mutex.Lock()
	c.nextId++
	c.mutex.Unlock()

	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	var responseKey = fmt.Sprintf("%d", request.Identifier)
	responseChannel, err := c.createResponseFuture(responseKey)
	if err != nil {
		return nil, err
	}
	if err := c.socket.WriteMessage(websocket.TextMessage, data); err != nil {
		return nil, err
	}

	defer c.deleteResponseFuture(responseKey)

	response, ok := <-responseChannel
	if !ok {
		return nil, fmt.Errorf("Call to server returned no result")
	}
	if len(response.Error) > 0 {
		return nil, fmt.Errorf("%s", response.Error)
	}
	return response.Result, nil
}

func (c *Client) Connect(scheme, host string, hubs []string) error {
	// Negotiate parameters.
	params, err := negotiate(scheme, host)
	if err != nil {
		return err
	}
	c.params = params

	// Connect Websocket.
	ws, err := connectWebsocket(host, c.params, hubs)
	if err != nil {
		return err
	}
	c.socket = ws

	var connectedChannel = make(chan bool)
	go c.dispatch(connectedChannel)

	select {
	case <-connectedChannel:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("connection timeout")
	}
}

func (c *Client) Close() error {
	return c.socket.Close()
}

func NewWebsocketClient(logf func(...interface{})) *Client {
	return &Client{
		nextId:          1,
		responseFutures: make(map[string]chan *serverMessage),
		logf:            logf,
	}
}
