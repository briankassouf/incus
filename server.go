package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"code.google.com/p/go.net/websocket"
)

type Server struct {
	ID     string
	Config *Configuration
	Store  *Storage

	timeout time.Duration
}

func createServer(conf *Configuration, store *Storage) *Server {
	hash := md5.New()
	io.WriteString(hash, time.Now().String())
	id := string(hash.Sum(nil))

	timeout := time.Duration(conf.GetInt("connection_timeout"))
	return &Server{id, conf, store, timeout}
}

func (this *Server) initSocketListener() {
	Connect := func(ws *websocket.Conn) {
		defer func() {
			if DEBUG {
				log.Println("Socket Closed")
			}
		}()

		sock := newSocket(ws, nil, this, "")

		if DEBUG {
			log.Printf("Socket connected via %s\n", ws.RemoteAddr())
		}
		if err := sock.Authenticate(""); err != nil {
			if DEBUG {
				log.Printf("Error: %s\n", err.Error())
			}
			return
		}

		go sock.listenForMessages()
		go sock.listenForWrites()

		if this.timeout <= 0 { // if timeout is 0 then wait forever and return when socket is done.
			<-sock.done
			return
		}

		select {
		case <-time.After(this.timeout * time.Second):
			sock.Close()
			return
		case <-sock.done:
			return
		}
	}

	http.Handle("/socket", websocket.Handler(Connect))
}

func (this *Server) initLongPollListener() {
	LpConnect := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if DEBUG {
				log.Println("Socket Closed")
			}
		}()

		sock := newSocket(nil, w, this, "")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store, no-cache, must-revalidate, post-check=0, pre-check=0")
		w.Header().Set("Connection", "keep-alive")
		//w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)

		if DEBUG {
			log.Printf("Long poll connected via \n")
		}

		if err := sock.Authenticate(r.FormValue("user")); err != nil {
			if DEBUG {
				log.Printf("Error: %s\n", err.Error())
			}
			return
		}

		page := r.FormValue("page")
		if page != "" {
			if sock.Page != "" {
				this.Store.UnsetPage(sock) //remove old page if it exists
			}

			sock.Page = page
			this.Store.SetPage(sock)
		}

		command := r.FormValue("command")
		if command != "" {
			var cmd CommandMsg
			json.Unmarshal([]byte(command), &cmd)

			go cmd.FromSocket(sock)
		}

		go sock.listenForWrites()

		select {
		case <-time.After(30 * time.Second):
			sock.Close()
			return
		case <-sock.done:
			return
		}
	}

	http.HandleFunc("/lp", LpConnect)
}

func (this *Server) initAppListener() {
	rec := make(chan []string)
	consumer, err := this.Store.redis.Subscribe(rec, this.Config.Get("redis_message_channel"))
	if err != nil {
		log.Fatal("Couldn't subscribe to redis channel")
	}
	defer consumer.Quit()

	if DEBUG {
		log.Println("LISENING FOR REDIS MESSAGE")
	}
	var ms []string
	for {
		ms = <-rec

		var cmd CommandMsg
		json.Unmarshal([]byte(ms[2]), &cmd)
		go cmd.FromRedis(this)
	}
}

func (this *Server) initPingListener() {
	pingHandler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "OK")
	}

	http.HandleFunc("/ping", pingHandler)
}

func (this *Server) sendHeartbeats() {

	for {
		time.Sleep(20 * time.Second)

		clients := this.Store.Clients()

		for _, user := range clients {
			for _, sock := range user {

				if sock.isWebsocket() {
					websocket.Message.Send(sock.ws, "")
				}

			}
		}
	}
}
