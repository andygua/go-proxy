package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
)

//Redeclare it to smain for now.
func smain() {

	http.HandleFunc("/", handler)

	// register the handler and deliver requests to it
	err := http.ListenAndServe(":8000", nil)
	checkError(err)
}

var counter int

func handler(writer http.ResponseWriter, req *http.Request) {
	counter += 1
	count := strconv.Itoa(counter)
	writer.Write([]byte("Test respose from Server " + count))
}

func checkError(err error) {
	if err != nil {
		fmt.Println("Fatal error ", err.Error())
		os.Exit(1)
	}
}
