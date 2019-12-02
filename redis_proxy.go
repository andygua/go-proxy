package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

//ConfigMap A map of name of the proxy vs its actually backend endpoint
var ConfigMap map[string]*Entry

//Config json config structure for the proxy
type Config struct {
	sync.Mutex `json:"-"`
	HTTPPort   string  //HTTPPort server Port number that we should bind to
	Entries    []Entry //Entries List of proxy entries
}

//Entry Representation of each entry in the proxy config
type Entry struct {
	Name          string
	Pair          PorxyPair
	Delay         int64        // delay before read can begin, in milli second
	Bandwidth     int64        // Bandwidth in KB/sec
	DisConnection Connection   // time after first accept, in Seconds for disconnection (start, stop)
	CloseConn     chan bool    `json:"-"` //CloseConn a singal in this channel will close the connection
	l             net.Listener `json:"-"` //
	pairs         []FTPair     `json:"-"`
}

func (e *Entry) WaitToClose() {
	log.Printf("WaitToClose() started")
	defer log.Printf("WaitToClose() ended")
	<-e.CloseConn
	//Stop accepting new connections
	e.l.Close()
	//Close all the existing connections almost immidealty
	timout := time.Now().Add(time.Microsecond)
	for _, p := range e.pairs {
		p.F.SetDeadline(timout)
		p.T.SetDeadline(timout)
	}
}

//PorxyPair The actuall proxy pair from (bind port) to actual port
type PorxyPair struct {
	From string //IP:PORT pair
	To   string //IP:PORT pair
}

//HTTPUpdate This structure is used by the HTTP PUT request to change the IP address of the destination on the fly
type HTTPUpdate struct {
	Name string
	Addr string
}

type Connection struct {
	Enabled bool
	Start   []int64
	End     []int64
}
type ConnectionState struct {
	State  string
	Start  time.Time
	Length int
	Index  int
}

type FTPair struct {
	F, T net.Conn
}

var GlobalConfig *Config

//HandleConnection Actuall proxy implementation per client. Untimatly this performs a implments a duplex io.Copy
func HandleConnection(E *Entry) error {

	var CurrentE *Entry //A Temp variable to get the latest Desination proxy value
	var OK bool

	E.pairs = []FTPair{}
	E.CloseConn = make(chan bool)
	go E.WaitToClose()

	log.Printf("HandleConnection() %v", E)
	src, err := net.Listen("tcp", E.Pair.From)
	if err != nil {
		log.Printf("Error binding to the IP %v", err)
		return err
	}
	defer src.Close()

	//Add this in the global Map so that it can be updated dynamically by HTTP apis
	ConfigMap[E.Name] = E

	state := ConnectionState{}
	if E.DisConnection.Enabled {
		state.State = "ENABLED"
	}

	E.l = src

	for {

		conn, err := src.Accept()
		if err != nil {
			log.Printf("Error accepting a new connection %v %T", err, err)
			return err
		}

		if E.DisConnection.Enabled {
			if state.State == "ENABLED" {
				state.Start = time.Now()
				state.State = "CONNECT"
				//Input error checks
				state.Length = len(E.DisConnection.Start)
				if state.Length == 0 || state.Length != len(E.DisConnection.End) {
					log.Printf("Invalid inputs for DISCONNECTION length")
					state.State = "DISABLED"
				} else {
					for i, sTime := range E.DisConnection.Start {
						if sTime > E.DisConnection.End[i] {
							log.Printf("Invalid inputs for DISCONNECTION array")
							state.State = "DISABLED"
							break
						}
					}
					state.Length = len(E.DisConnection.Start)
				}
			}
			// Check if we entered in disconnect
			if state.State == "CONNECT" && state.Length > state.Index {
				startDisConnect := time.Duration(E.DisConnection.Start[state.Index]) * time.Second
				if time.Since(state.Start) >= startDisConnect {
					state.State = "DISCONNECT"
				}
			}
			// Handle disconnect
			if state.State == "DISCONNECT" && state.Length > state.Index {
				endDisConnect := time.Duration(E.DisConnection.End[state.Index]) * time.Second

				if time.Since(state.Start) < endDisConnect {
					duration := endDisConnect - time.Since(state.Start)
					if duration > 0 {
						time.Sleep(time.Duration(duration))
					}
				}
				state.Index++
				state.State = "CONNECT"
			}
			//Get the latest Entry from the MAP because it migh thave been updated on the fly.
		}

		if CurrentE, OK = ConfigMap[E.Name]; !OK {
			log.Printf("Error Proxy entry is incorrect / empty for %s", E.Name)
			conn.Close()
			continue
		}

		//Start a Lamda for performing the proxy
		//F := From Connection
		//T := To Connection
		//This proxy will simply transfer everything from F to T net.Conn
		go func(E *Entry, F net.Conn) {
			T, err := net.Dial("tcp", E.Pair.To)
			if err != nil {
				log.Printf("Unable to connect to the Destination %s %v", E.Pair.To, err)
				return
			}
			defer T.Close()
			defer F.Close()

			E.pairs = append(E.pairs, FTPair{F: F, T: T})
			// 1 min timeout for normal
			timeout := time.Duration(60 * time.Second)
			if state.State == "CONNECT" && state.Length > state.Index {
				endConnect := time.Duration(E.DisConnection.Start[state.Index]) * time.Second
				timeout = endConnect - time.Since(state.Start)
				if timeout < 0 {
					timeout = 0
				}
			}
			if E.DisConnection.Enabled {
				T.SetDeadline(time.Now().Add(timeout))
				F.SetDeadline(time.Now().Add(timeout))
			}

			ftChan := make(chan string)
			tfChan := make(chan string)
			go ioCopy(F, T, E.Delay, E.Bandwidth, ftChan)
			go ioCopy(T, F, E.Delay, E.Bandwidth, tfChan)

			<-tfChan
			<-ftChan
		}(CurrentE, conn)
	}
}

func ioCopy(dst io.Writer, src io.Reader, delay int64, rate int64, c chan string) (written int64, err error) {

	var buf []byte
	var sleep time.Duration = 0

	size := 32 * 1024
	if l, ok := src.(*io.LimitedReader); ok && int64(size) > l.N {
		fmt.Println("ioCopy: LimitedReader size > l.N")

		if l.N < 1 {
			size = 1
		} else {
			size = int(l.N)
		}
	}

	if buf == nil {
		buf = make([]byte, size)
	}
	for {
		// Delay specified milli sec
		if delay != 0 {
			delay := time.Duration(delay)
			time.Sleep(delay * time.Millisecond)
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			if rate <= 0 {
				sleep = 0
			} else {
				sleep = time.Duration(nr) * time.Millisecond / time.Duration(rate)
			}
			time.Sleep(sleep)
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	c <- "done"
	return written, err
}

//HandleHTTPUpdate Call beack to handle /Update/ HTTP call back
func HandleHTTPUpdate(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hi there, Going to Update %s! Method=%s\n", r.URL.Path[1:], r.Method)
	if r.Method == "PUT" {
		//This can be used for updating an existing variable
		content, err := ioutil.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			fmt.Fprintf(w, "Error understanding the Body %v", err)
			log.Printf("Error understanding the Body %v", err)
			return
		}

		var val HTTPUpdate
		var CurrentE *Entry
		var OK bool
		err = json.Unmarshal(content, &val)
		if err != nil {
			fmt.Fprintf(w, "Wrong json format %v", err)
			log.Printf("Wrong json format %v", err)
			return
		}
		if CurrentE, OK = ConfigMap[val.Name]; !OK {
			log.Printf("Error Proxy entry is incorrect / empty for %s", val.Name)
			fmt.Fprintf(w, "Error Proxy entry is incorrect / empty for %s", val.Name)
			return
		}
		log.Printf("Updating From porxy for %s From %s TO %s", val.Name, CurrentE.Pair.To, val.Addr)
		CurrentE.Pair.To = val.Addr
		ConfigMap[val.Name] = CurrentE
		return
	}
	return
}

//HandleHTTPGet call back to handle /Get/ HTTP callback
func HandleHTTPGet(w http.ResponseWriter, r *http.Request) {
	retBytes, err := json.MarshalIndent(ConfigMap, " ", "  ")
	if err != nil {
		log.Printf("Error Marshalling HandleHTTPGet() %v", err)
		fmt.Fprintf(w, "Error Marshalling HandleHTTPGet() %v", err)
		return

	}
	fmt.Fprintf(w, "Current Config: %s", string(retBytes))
	return
}

//HandleDisconnect will be used when we recive a HTTP disconnect call
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {

	log.Printf("Attempting to disconnect")
	StopConnectionHandlers(GlobalConfig)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Disconnected\n")
}

//HandleReConnect will be triggered when we recive HTTP reconnect call
func HandleReConnect(w http.ResponseWriter, r *http.Request) {
	StartConnectionHandlers(GlobalConfig)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Reconnected\n")
}

func StartConnectionHandlers(cfg *Config) {
	// Hanlde each connection
	cfg.Lock()
	defer cfg.Unlock()

	for _, E := range cfg.Entries {
		go HandleConnection(&E)
	}
}

func StopConnectionHandlers(cfg *Config) {
	// Hanlde each connection
	cfg.Lock()
	defer cfg.Unlock()

	for _, E := range ConfigMap {
		log.Printf("Singalling Entry e=%s", E.Name)
		E.CloseConn <- true
		log.Printf("Singaled Entry e=%s", E.Name)
	}
}

func main() {

	var Cfg Config

	//Initialize the global Config map
	ConfigMap = make(map[string]*Entry)

	//Read a config file that has json update the config files
	cfgFileName := flag.String("f", "./config.json", "Supply the location of MrRedis configuration file")
	flag.Parse()

	log.Printf("The config file name is %s ", *cfgFileName)
	cfgFile, err := ioutil.ReadFile(*cfgFileName)

	if err != nil {
		log.Printf("Error Reading the configration file. Resorting to default values")
	}
	err = json.Unmarshal(cfgFile, &Cfg)
	if err != nil {
		log.Fatalf("Error parsing the config file %v", err)
		return
	}
	log.Printf("Configuration file is = %v", Cfg)

	StartConnectionHandlers(&Cfg)

	GlobalConfig = &Cfg

	http.HandleFunc("/Update/", HandleHTTPUpdate)
	http.HandleFunc("/Get/", HandleHTTPGet)
	http.HandleFunc("/Disconnect/", HandleDisconnect)
	http.HandleFunc("/Connect/", HandleReConnect)
	log.Fatal(http.ListenAndServe(":"+Cfg.HTTPPort, nil))

	//Wait indefinitely
	waitCh := make(chan bool)
	<-waitCh

}
