/* matrix-push.go - A Matrix Push Gateway */

/*
 *  Copyright (c) 2016 Sergio L. Pascual <slp@sinrega.org>
 *  All rights reserved.
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License
 *  along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log/syslog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"sync"
	"time"
)

type Logger struct {
	Ready bool
	Slog  *syslog.Writer
}

func (logger *Logger) init() {
	var err error
	logger.Slog, err = syslog.New(syslog.LOG_INFO, "matrix-pushgw")
	if err != nil {
		fmt.Println("Error opening syslog")
		logger.Ready = false
	} else {
		logger.Ready = true
	}
}

func (logger *Logger) error(m string) {
	logger.Slog.Notice("ERROR: " + m)
}

func (logger *Logger) info(m string) {
	logger.Slog.Notice("INFO: " + m)
}

func (logger *Logger) notice(m string) {
	logger.Slog.Notice("NOTICE: " + m)
}

func (logger *Logger) debug(m string) {
	if gConfig.Debug {
		logger.Slog.Notice("DEBUG: " + m)
	}
}

func (logger *Logger) debugWS(m string) {
	if gConfig.DebugWS {
		logger.Slog.Debug("DEBUGWS: " + m)
	}
}

type Content struct {
	Body           string
	Format         string
	Formatted_body string
	Msgtype        string
}

type Counts struct {
	Unread int
}

type DeviceData struct {
}

type Tweaks struct {
	Highlight  bool
	Sound      string
}

type Device struct {
	App_id     string
	Data       DeviceData
	Pushkey    string
	Pushkey_ts int
	Tweaks     Tweaks
}

type Notification struct {
	Content             Content
	Counts              Counts
	Devices             []Device
	Event_id            string
	Id                  string
	Room_id             string
	Sender              string
	Sender_display_name string
	Type                string
}

type PushNotification struct {
	Notification Notification
}

type DevMsg struct {
	MsgType int
	Error   bool
}

type DevConn struct {
	Id    string
	Conn  net.Conn
	Bufrw *bufio.ReadWriter
	Chan  chan *DevMsg
	Dead  bool
}

func (devConn *DevConn) tcpReader() {
	buffer := make([]byte, 4096)
	//reader := bufio.NewReader(devConn.

	for {
		msg := new(DevMsg)
		msg.MsgType = 0

		fmt.Println("Reading from TCP socket")
		_, err := devConn.Bufrw.Read(buffer)
		if err != nil {
			fmt.Println("Error reading from TCP socket: " + err.Error())
			logger.error("Error reading from TCP socket: " + err.Error())
			msg.Error = true
			devConn.Chan <- msg
			return
		}
	}
}

func (devConn *DevConn) pinger() {
	for {
		time.Sleep(5 * time.Second)
		if devConn.Dead {
			fmt.Println("devConn is dead, bailing out")
			return
		}
		fmt.Println("Sending PING")
		devConn.Bufrw.WriteString("PING\r\n")
		devConn.Bufrw.Flush()
	}
}

func (devConn *DevConn) sendPush() {
	fmt.Println("Sending PUSH")
	devConn.Bufrw.WriteString("PUSH\r\n")
	devConn.Bufrw.Flush()
}

func (devConn *DevConn) start() {
	logger.debug("devConn start")

	devConn.Chan = make(chan *DevMsg)
	go devConn.tcpReader()
	go devConn.pinger()

	for {
		msg := <- devConn.Chan
		if msg.Error {
			fmt.Println("Connection returned error")
			devConn.Conn.Close()
			devConn.Dead = true
			devToConnMap.Lock()
			delete(devToConnMap.m, devConn.Id)
			devToConnMap.Unlock()
			// TODO: Remove device ID from map
			return
		}
	}
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	logger.debug("handleRegister")
}

func handleListen(w http.ResponseWriter, r *http.Request) {
	logger.debug("handleResgister")
	type ListenRequest struct {
		DevId string
	}

	if r.Method != "POST" {
		logger.error("handleListen without POST")
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	bodybytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logger.error(err.Error())
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	body := string(bodybytes)
	dec := json.NewDecoder(strings.NewReader(body))
	var lr ListenRequest
	err = dec.Decode(&lr)
	if err != nil {
		logger.error(err.Error())
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		logger.error("webserver doesn't support hijacking")
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		logger.error("error hijacking session")
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	var devConn DevConn
	devConn.Id = lr.DevId
	devConn.Conn = conn
	devConn.Bufrw = bufrw

	devToConnMap.Lock()
	devToConnMap.m[lr.DevId] = devConn
	devToConnMap.Unlock()

	go devConn.start()
}

func handlePush(w http.ResponseWriter, r *http.Request) {
	logger.debug("handlePush")

	bodybytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logger.error(err.Error())
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	body := string(bodybytes)

	fmt.Println("handlePush")
	fmt.Println(body)

	dec := json.NewDecoder(strings.NewReader(body))
	var n PushNotification
	err = dec.Decode(&n)
	if err != nil {
		fmt.Println("Error parsing JSON")
		logger.error(err.Error())
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	fmt.Println("Iterating through device list")

	for _, d := range n.Notification.Devices {
		fmt.Println(d.Pushkey)
		devToConnMap.RLock()
		if devConn, ok := devToConnMap.m[d.Pushkey]; ok {
			devToConnMap.RUnlock()
			devConn.sendPush()
		} else {
			devToConnMap.RUnlock()
		}
	}

	fmt.Println("done")

	w.Write([]byte("{}"))
}

func generateUUID() string {
	u := make([]byte, 16)
	_, err := rand.Read(u)
	if err != nil {
		return "fakeuuidbecauserandfailed"
	}

	u[8] = (u[8] | 0x80) & 0xBF // what's the purpose ?
	u[6] = (u[6] | 0x40) & 0x4F // what's the purpose ?
	return hex.EncodeToString(u)
}


type Config struct {
	PlainPort      int
	SslPort        int
	Debug          bool
	DebugWS        bool
}

func listenPlainHTTP(wg *sync.WaitGroup) {
	defer wg.Done()

	if gConfig.PlainPort == 0 {
		fmt.Println("Plain HTTP port not configured")
		return
	}

	err := http.ListenAndServe(":"+strconv.Itoa(gConfig.PlainPort), nil)

	if err != nil {
		fmt.Println("Can't listen on plain HTTP port:", err.Error())
	}
}

func signalHandler(c *chan os.Signal) {
	for s := range *c {
		if s == syscall.SIGHUP {
			logger.notice("Received " + s.String() + " signal, reloading")
		} else {
			logger.notice("Received " + s.String() + " signal, bailing out")
			os.Exit(0)
		}
	}
}

var gConfig *Config
var logger *Logger

var devToConnMap = struct {
	sync.RWMutex
	m map[string]DevConn
} {m: make(map[string]DevConn)}

func main() {
	file, _ := os.Open("matrix-pushgw.conf")
	defer file.Close()
	decoder := json.NewDecoder(file)
	config := new(Config)

	err := decoder.Decode(config)
	if err != nil {
		fmt.Println("Can't open configuration file:", err)
	} else {
		gConfig = config
		signal_channel := make(chan os.Signal, 1)
		signal.Notify(signal_channel, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		go signalHandler(&signal_channel)

		logger = new(Logger)
		logger.init()

		http.HandleFunc("/register", handleRegister)
		http.HandleFunc("/listen", handleListen)
		http.HandleFunc("/_matrix/push/r0/notify", handlePush)

		var wg sync.WaitGroup
		wg.Add(2)

		go listenPlainHTTP(&wg)

		wg.Wait()
	}

	os.Exit(0)
}
