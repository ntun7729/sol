package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"log"
	"sync"

	"github.com/ntun7729/sol/internal/mobileclient"
)

var (
	clientMu sync.Mutex
	client   *mobileclient.Client
)

//export sol_start
func sol_start(serverURL *C.char, token *C.char) C.int {
	clientMu.Lock()
	defer clientMu.Unlock()

	if client != nil {
		return 0
	}

	c, err := mobileclient.Start(C.GoString(serverURL), C.GoString(token), "127.0.0.1:1080")
	if err != nil {
		log.Printf("android SOL core start failed: %v", err)
		return 1
	}
	client = c
	return 0
}

//export sol_stop
func sol_stop() {
	clientMu.Lock()
	defer clientMu.Unlock()
	if client != nil {
		client.Close()
		client = nil
	}
}

//export sol_running
func sol_running() C.int {
	clientMu.Lock()
	defer clientMu.Unlock()
	if client != nil {
		return 1
	}
	return 0
}

func main() {}
