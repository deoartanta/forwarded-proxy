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
	"time"

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
	wg.Wait()

	wg.Go(func() {
		err := connectSocksSSH("sbox-cda")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := connectSocks("stg-redis")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := connectSocksSSH("prod-sdn-redis")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := connectSocks("stg")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := startTunnel("prod-sdn-redis")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := startTunnel("sbox-sdn")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := startTunnel("sbox-cda")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := startTunnel("stg-redis")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()

	wg.Go(func() {
		err := startTunnel("stg")
		if err != nil {
			log.Println(logRed(err.Error()))
		}
	})
	wg.Wait()
}

func connectSocks(key string) error {
	viaSocks := GetConfig("remote." + key + ".via-socks")
	status := GetConfig("remote." + key + ".status")
	if status == "in-active" || status == "" {
		return fmt.Errorf("[socks-%s] status %s", key, status)
	}
	if viaSocks != "active" || viaSocks == "" {
		return fmt.Errorf("[socks-%s] via socks %s", key, viaSocks)
	}
	remoteIp := GetConfig("remote." + key + ".ip")
	remotePort := GetConfig("remote." + key + ".port")
	remotePortLocal := GetConfig("remote." + key + ".port-local")

	local := "127.0.0.1:" + remotePortLocal
	remote := remoteIp + ":" + remotePort

	socksIp := GetConfig("socks.ip")
	socksPort := GetConfig("socks.port")

	socks := socksIp + ":" + socksPort
	log.Printf("[socks-"+key+"] Trying SOCKS %s -> %s via %s\n", local, remote, socks)

	ln, err := net.Listen("tcp", local)
	if err != nil {
		return fmt.Errorf("[socks-"+key+"] %v", err)
	}
	log.Println("["+logGreen("socks-"+key)+"] Listening", logGreen(local), "->", logGreen(remote), "via", logGreen(socks))
	mapIp := map[string]bool{}
	for {
		client, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("[socks-"+key+"] %v", err)
		}
		go func(cl net.Conn) {
			srv, err := socks5Dial(socks, remote)
			if err != nil {
				log.Println("["+logGreen("socks-"+key)+"]", logRed(err.Error()))
				cl.Close()
				return
			}
			ip := ""
			ipSplit := strings.Split(srv.LocalAddr().String(), ":")
			if len(ipSplit) > 0 {
				ip = ipSplit[0]
			}
			if !mapIp[ip] {
				log.Println("["+logGreen("socks-"+key)+"] new client address=>", logGreen(ip))
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
	viaSocks := GetConfig("remote." + key + ".via-socks")
	status := GetConfig("remote." + key + ".status")
	if status == "in-active" || status == "" {
		return fmt.Errorf("[socks-%s] status %s", key, status)
	}
	if viaSocks != "active" || viaSocks == "" {
		return fmt.Errorf("[socks-%s] via socks %s", key, viaSocks)
	}
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
	log.Printf("[socks-"+key+"] Trying SOCKS %s -> SSH %s\n", proxyAddr, sshServer)

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return fmt.Errorf("[socks-"+key+"] SOCKS dialer failed: %v", err)
	}

	conn, err := dialer.Dial("tcp", sshServer)
	if err != nil {
		return fmt.Errorf("[socks-"+key+"] SOCKS connect failed: %v", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, sshServer, sshConfig)
	if err != nil {
		return fmt.Errorf("[socks-"+key+"] SSH handshake failed: %v", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	// log.Println("["+key+"]SSH connected!", key)

	ln, err := net.Listen("tcp", local)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	log.Println("["+logGreen("socks-"+key)+"] Listening", logGreen(local), "->", logGreen(remote), "via", logGreen(proxyAddr), "->", "ssh", logGreen(sshServer))
	mapIp := map[string]bool{}

	for {
		local, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("["+logGreen("socks-"+key)+"] %v", logRed(err.Error()))
		}
		remoteDB, err := client.Dial("tcp", remote)
		if err != nil {
			log.Printf("["+logGreen("socks-"+key)+"] %v\n", logRed(err.Error()))
			local.Close()
			continue
		}
		ip := ""
		ipSplit := strings.Split(remoteDB.LocalAddr().String(), ":")
		if len(ipSplit) > 0 {
			ip = ipSplit[0]
		}
		if !mapIp[ip] {
			log.Println("["+logGreen("socks-"+key)+"] new client address with ssh=>", logGreen(ip))
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
func connectSSHV2(sshServer string, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {

	conn, err := net.DialTimeout("tcp", sshServer, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("SSH dial failed: %w", err)
	}

	// Timeout untuk proses SSH handshake.
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	sshConn, chans, reqs, err := ssh.NewClientConn(
		conn,
		sshServer,
		sshConfig,
	)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH handshake failed: %w", err)
	}

	// Hapus deadline setelah handshake berhasil.
	conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func forward(client *ssh.Client, localConn net.Conn, remoteAddr string) {
	defer localConn.Close()

	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("Remote dial failed: %v", err)
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{})

	go func() {
		io.Copy(remoteConn, localConn)
		close(done)
	}()

	io.Copy(localConn, remoteConn)

	<-done
}

func startTunnel(key string) error {
	viaSocks := GetConfig("remote." + key + ".via-socks")
	status := GetConfig("remote." + key + ".status")
	if status == "in-active" || status == "" {
		return fmt.Errorf("[ssh-%s] status %s", key, status)
	}
	if viaSocks == "active" || viaSocks == "" {
		return fmt.Errorf("[ssh-%s] via socks %s", key, viaSocks)
	}

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

	sshConfig := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            []ssh.AuthMethod{ssh.Password(sshPass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	localAddr := "127.0.0.1:" + remotePortLocal
	remoteAddr := remoteIp + ":" + remotePort

	// Listener hanya dibuat satu kali.
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("listen failed: %w", err)
	}
	defer listener.Close()
	log.Printf("[%s] Listening %s -> %s via SSH %s\n", logGreen("ssh-"+key), localAddr, remoteAddr, sshServer)

	accepted := make(chan net.Conn, 100)

	// Terima koneksi lokal secara terus-menerus.
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Printf("[%s] Accept failed: %v\n", logGreen("ssh-"+key), logRed(err.Error()))
				return
			}

			select {
			case accepted <- conn:
			default:
				log.Printf("[%s] %s\n", logGreen("ssh-"+key), logRed("Connection queue full, closing connection"))
				conn.Close()
			}
		}
	}()

	for {
		log.Printf("[%s] %s\n", logGreen("ssh-"+key), logGreen("Attempting SSH connection..."))

		client, err := connectSSHV2(sshServer, sshConfig)
		if err != nil {
			log.Printf("[%s] SSH connection failed: %v", logGreen("ssh-"+key), logRed(err.Error()))
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("[%s] %s\n", logGreen("ssh-"+key), logGreen("SSH connected successfully"))

		// Memantau koneksi SSH.
		disconnected := make(chan error, 1)

		go func() {
			disconnected <- client.Wait()
		}()

	loopListener:
		for {
			select {
			case localConn := <-accepted:
				go forward(client, localConn, remoteAddr)

			case err := <-disconnected:
				log.Printf("[%s] SSH disconnected: %s", logGreen("ssh-"+key), logRed(err.Error()))
				break loopListener
			}
		}

		client.Close()

		log.Printf("[%s] %s", logGreen("ssh-"+key), "Retry SSH connection in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

// func connectSSH(key string) error {
// 	viaSocks := GetConfig("remote." + key + ".via-socks")
// 	status := GetConfig("remote." + key + ".status")
// 	if status == "in-active" || status == "" {
// 		return fmt.Errorf("[ssh-%s] status %s", key, status)
// 	}
// 	if viaSocks == "active" || viaSocks == "" {
// 		return fmt.Errorf("[ssh-%s] via socks %s", key, viaSocks)
// 	}
// 	sshIpServer := GetConfig("remote." + key + ".ssh-ip")
// 	sshPortServer := GetConfig("remote." + key + ".ssh-port")
// 	sshUserServer := GetConfig("remote." + key + ".ssh-user")
// 	sshPassServer := GetConfig("remote." + key + ".ssh-password")

// 	remoteIp := GetConfig("remote." + key + ".ip")
// 	remotePort := GetConfig("remote." + key + ".port")
// 	remotePortLocal := GetConfig("remote." + key + ".port-local")

// 	sshServer := sshIpServer + ":" + sshPortServer
// 	sshUser := sshUserServer
// 	sshPass := sshPassServer

// 	local := "127.0.0.1:" + remotePortLocal
// 	remote := remoteIp + ":" + remotePort

// 	sshConfig := &ssh.ClientConfig{
// 		User:            sshUser,
// 		Auth:            []ssh.AuthMethod{ssh.Password(sshPass)},
// 		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
// 	}

// 	log.Printf("[ssh-%s] Direct connect to SSH %s...", key, sshServer)
// 	conn, err := net.Dial("tcp", sshServer)
// 	if err != nil {
// 		return fmt.Errorf("[ssh-"+key+"] SSH dialer failed: %v", err)
// 	}

// 	sshConn, chans, reqs, err := ssh.NewClientConn(conn, sshServer, sshConfig)
// 	if err != nil {
// 		return fmt.Errorf("[ssh-"+key+"] SSH handshake failed: %v", err)
// 	}
// 	client := ssh.NewClient(sshConn, chans, reqs)

// 	listener, err := net.Listen("tcp", local)
// 	if err != nil {
// 		return fmt.Errorf("[ssh-"+key+"] listen failed: %v", err)
// 	}
// 	log.Println("["+logGreen("ssh-"+key)+"] Listening", logGreen(local), "->", logGreen(remote), "via ssh", logGreen(sshServer))
// 	mapIp := map[string]bool{}

// 	for {
// 		local, err := listener.Accept()
// 		if err != nil {
// 			return fmt.Errorf("[ssh-"+logGreen(key)+"] %v", logRed(err.Error()))
// 		}
// 		remoteDB, err := client.Dial("tcp", remote)
// 		if err != nil {
// 			log.Printf("["+logGreen("ssh-"+key)+"] %v\n", logRed(err.Error()))
// 			local.Close()
// 			continue
// 		}
// 		ip := ""
// 		ipSplit := strings.Split(remoteDB.LocalAddr().String(), ":")
// 		if len(ipSplit) > 0 {
// 			ip = ipSplit[0]
// 		}
// 		if !mapIp[ip] {
// 			log.Println("["+logGreen("ssh-"+key)+"] new client address with ssh=>", logGreen(ip))
// 			mapIp[ip] = true
// 		}
// 		go func() {
// 			go io.Copy(remoteDB, local)
// 			io.Copy(local, remoteDB)
// 			local.Close()
// 			remoteDB.Close()
// 		}()
// 	}
// }

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
