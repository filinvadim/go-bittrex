# go-bittrex
Signal-R client for Bittrex echange API V3

This version implement V3 Bittrex Signal-R API and the new HMAC authentification.

## Installation
```go get github.com/filinvadim/go-bittrex```
	
## Usage
~~~ go
package main

import (
	"fmt"
	"log"
	"time"
)

func main() {
	client, err := NewClientV3(fmt.Println)
	if err != nil {
		log.Fatal(err)
	}
	client.SetResponseHandler(func(hub, method string, arguments [][]byte) {
		fmt.Println("Message Received: ")
		fmt.Println("HUB: ", hub)
		fmt.Println("METHOD: ", method)
		fmt.Println("ARGUMENTS: ")
		for _, arg := range arguments {
			fmt.Println("    ", string(arg))
		}
	})
	client.SetErrorHandler(func(err error) {
		fmt.Println(err)
	})
	err = client.Authenticate(key, secret)  // your credentials
	if err != nil {
		log.Fatal(err)
	}
	err = client.Subscribe("ticker_BTC-USD")
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(5*time.Second)
	
  	client.Close()
}
~~~
