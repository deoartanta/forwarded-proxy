package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

func socks5Dial(socksAddr, targetAddr string) (net.Conn, error) {
	c, err := net.Dial("tcp", socksAddr)
	if err != nil {
		return nil, err
	}

	// handshake SOCKS5 no-auth
	c.Write([]byte{0x05, 0x01, 0x00})
	buf := make([]byte, 2)
	io.ReadFull(c, buf)

	// request CONNECT ke target
	host, portStr, _ := net.SplitHostPort(targetAddr)
	port, _ := net.LookupPort("tcp", portStr)
	ip := net.ParseIP(host).To4()
	if ip == nil { // kalau host bukan IP, pakai domain
		ip = net.ParseIP("0.0.0.0")
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(port))
	req = append(req, b...)
	c.Write(req)

	resp := make([]byte, 10)
	io.ReadFull(c, resp)
	return c, nil
}
func logGreen(str string) string {
	return fmt.Sprintf("\033[32m%s\033[0m", str)
}
func logRed(str string) string {
	return fmt.Sprintf("\033[31m%s\033[0m", str)
}

func main() {
	var wg sync.WaitGroup
	wg.Go(func() {
		err := connectSocksSSH("sbox-sdn")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})

	wg.Go(func() {
		err := connectSocksSSH("sbox-cda")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})

	wg.Go(func() {
		err := connectSocks("stg")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()
}

func connectSocks(key string) error {

	remoteIp := GetConfig("remote." + key + ".ip")
	remotePort := GetConfig("remote." + key + ".port")
	remotePortLocal := GetConfig("remote." + key + ".port-local")

	local := "127.0.0.1:" + remotePortLocal
	remote := remoteIp + ":" + remotePort

	socksIp := GetConfig("socks.ip")
	socksPort := GetConfig("socks.port")

	socks := socksIp + ":" + socksPort
	log.Printf("["+key+"] Trying SOCKS %s -> %s via %s\n", local, remote, socks)

	ln, err := net.Listen("tcp", local)
	if err != nil {
		return fmt.Errorf("["+key+"] %v", err)
	}
	log.Println("["+logGreen(key)+"] Listening", logGreen(local), "->", logGreen(remote), "via", logGreen(socks))
	mapIp := map[string]bool{}
	for {
		client, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("["+key+"] %v", err)
		}
		go func(cl net.Conn) {
			srv, err := socks5Dial(socks, remote)
			if err != nil {
				log.Println("["+logGreen(key)+"]", logRed(err.Error()))
				cl.Close()
				return
			}
			ip := ""
			ipSplit := strings.Split(srv.LocalAddr().String(), ":")
			if len(ipSplit) > 0 {
				ip = ipSplit[0]
			}
			if !mapIp[ip] {
				log.Println("["+logGreen(key)+"] new client address=>", logGreen(ip))
				mapIp[ip] = true
			}
			go io.Copy(srv, cl)
			io.Copy(cl, srv)
			cl.Close()
			srv.Close()
		}(client)
	}
}

func connectSocksSSH(key string) error {
	sshIpServer := GetConfig("remote." + key + ".ssh-ip")
	sshPortServer := GetConfig("remote." + key + ".ssh-port")
	sshUserServer := GetConfig("remote." + key + ".ssh-user")
	sshPassServer := GetConfig("remote." + key + ".ssh-password")

	remoteIp := GetConfig("remote." + key + ".ip")
	remotePort := GetConfig("remote." + key + ".port")
	remotePortLocal := GetConfig("remote." + key + ".port-local")

	sshServer := sshIpServer + ":" + sshPortServer
	sshUser := sshUserServer
	sshPass := sshPassServer
	local := "127.0.0.1:" + remotePortLocal
	remote := remoteIp + ":" + remotePort

	socksIp := GetConfig("socks.ip")
	socksPort := GetConfig("socks.port")

	proxyAddr := socksIp + ":" + socksPort

	// log.Println("SOCKS connected, now trying SSH handshake...")

	sshConfig := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            []ssh.AuthMethod{ssh.Password(sshPass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	log.Printf("["+key+"]Trying SOCKS %s -> SSH %s\n", proxyAddr, sshServer)

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return fmt.Errorf("["+key+"] SOCKS dialer failed: %v", err)
	}

	conn, err := dialer.Dial("tcp", sshServer)
	if err != nil {
		return fmt.Errorf("["+key+"] SOCKS connect failed: %v", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, sshServer, sshConfig)
	if err != nil {
		return fmt.Errorf("["+key+"] SSH handshake failed: %v", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	// log.Println("["+key+"]SSH connected!", key)

	ln, err := net.Listen("tcp", local)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	log.Println("["+logGreen(key)+"] Listening", logGreen(local), "->", logGreen(remote), "via ssh", logGreen(sshServer))
	mapIp := map[string]bool{}

	for {
		local, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("["+logGreen(key)+"] %v", logRed(err.Error()))
		}
		remoteDB, err := client.Dial("tcp", remote)
		if err != nil {
			log.Printf("["+logGreen(key)+"] %v\n", logRed(err.Error()))
			local.Close()
			continue
		}
		ip := ""
		ipSplit := strings.Split(remoteDB.LocalAddr().String(), ":")
		if len(ipSplit) > 0 {
			ip = ipSplit[0]
		}
		if !mapIp[ip] {
			log.Println("["+logGreen(key)+"] new client address with ssh=>", logGreen(ip))
			mapIp[ip] = true
		}
		go func() {
			go io.Copy(remoteDB, local)
			io.Copy(local, remoteDB)
			local.Close()
			remoteDB.Close()
		}()
	}
}

func GetConfig(key string) string {
	var current any = initConfig()

	for _, part := range strings.Split(key, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}

		current, ok = m[part]
		if !ok {
			return ""
		}
	}

	return fmt.Sprint(current)
}
func initConfig() (config map[string]any) {
	data, err := os.ReadFile("config.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(data, &config); err != nil {
		panic(err)
	}
	return
}
