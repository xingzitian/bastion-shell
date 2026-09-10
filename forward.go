package main

import (
	"fmt"
	"log"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// forwardRule 本地端口转发规则（对齐 Electron 版的 PortForwardRule）
type forwardRule struct {
	ID         string `json:"id"`
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
	RemoteHost string `json:"remoteHost"`
	RemotePort int    `json:"remotePort"`
}

type forwardEntry struct {
	rule     forwardRule
	connID   string
	listener net.Listener
}

var forwardMgr = struct {
	sync.Mutex
	m map[string]*forwardEntry
}{m: map[string]*forwardEntry{}}

// startForward 在指定 SSH 连接上启动一条本地转发（本地监听 → forwardOut 到远端）
func startForward(connID string, client *ssh.Client, rule forwardRule) error {
	forwardMgr.Lock()
	if _, exists := forwardMgr.m[rule.ID]; exists {
		forwardMgr.Unlock()
		return fmt.Errorf("转发已存在: %s", rule.ID)
	}
	forwardMgr.Unlock()

	addr := fmt.Sprintf("%s:%d", rule.LocalHost, rule.LocalPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	forwardMgr.Lock()
	forwardMgr.m[rule.ID] = &forwardEntry{rule: rule, connID: connID, listener: ln}
	forwardMgr.Unlock()

	remote := fmt.Sprintf("%s:%d", rule.RemoteHost, rule.RemotePort)
	log.Printf("[fw %s] 转发启动 %s -> %s", rule.ID, addr, remote)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleForward(conn, client, remote) // handleForward 在 main.go
		}
	}()
	return nil
}

// stopForward 停止一条转发
func stopForward(ruleID string) {
	forwardMgr.Lock()
	e, ok := forwardMgr.m[ruleID]
	if ok {
		delete(forwardMgr.m, ruleID)
	}
	forwardMgr.Unlock()
	if ok {
		_ = e.listener.Close()
		log.Printf("[fw %s] 转发已停止", ruleID)
	}
}

// stopForwardsOfConn 停止某连接上的全部转发（连接关闭时调用）
func stopForwardsOfConn(connID string) {
	forwardMgr.Lock()
	var ids []string
	for id, e := range forwardMgr.m {
		if e.connID == connID {
			ids = append(ids, id)
		}
	}
	forwardMgr.Unlock()
	for _, id := range ids {
		stopForward(id)
	}
}
