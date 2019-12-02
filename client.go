package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

const PROXY_URL = "http://localhost:6186"

func testDelay() {
	// Start timer
	start := time.Now()

	resp, err := http.Get(PROXY_URL)

	elapsed := time.Since(start)

	if err == nil {
		defer resp.Body.Close()
		body, er := ioutil.ReadAll(resp.Body)
		if er == nil {
			fmt.Println("The Response is = ", string(body))
		}
	} else {
		fmt.Println("http Get Failed, error = [" + err.Error() + "]")
		return
	}

	// Print delay
	log.Printf("Delay is : %s", elapsed)

}

func cmain() {

	for i := 0; i < 5; i++ {
		testDelay()
	}

}
