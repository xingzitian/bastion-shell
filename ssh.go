package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// connect 建立 SSH 连接（密码 / 私钥 / keyboard-interactive MFA，非交互式）。
func connect(host string, port int, user, password, key, passphrase, mfa string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 正式版接 TOFU
		Timeout:         15 * time.Second,
	}
	if key != "" {
		signer, err := loadKey(key, passphrase)
		if err != nil {
			return nil, fmt.Errorf("加载私钥: %w", err)
		}
		config.Auth = append(config.Auth, ssh.PublicKeys(signer))
	}
	if password != "" {
		if mfa != "" {
			// 走 keyboard-interactive：密码类提示答密码，其余答 MFA
			config.Auth = append(config.Auth, ssh.KeyboardInteractive(kbi(password, mfa)))
		}
		config.Auth = append(config.Auth, ssh.Password(password))
	}
	return dialSSH(fmt.Sprintf("%s:%d", host, port), config)
}

// dialSSH 建立 SSH 连接并启用 keep-alive（TCP + SSH 心跳），防止空闲被服务器/NAT 掐断。
func dialSSH(addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	client := ssh.NewClient(c, chans, reqs)
	// SSH 级 keep-alive：每 30s 发 keepalive 请求，失败则关闭连接
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = client.Close()
				return
			}
		}
	}()
	return client, nil
}

// kbi 对应 Electron 版的 wrapAskAuth：密码提示自动应答密码，其余（MFA）答 mfa。
func kbi(password, mfa string) func(user, instruction string, questions []string, echos []bool) ([]string, error) {
	return func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i, q := range questions {
			lq := strings.ToLower(q)
			if strings.Contains(lq, "password") || strings.Contains(lq, "密码") || strings.Contains(lq, "口令") {
				answers[i] = password
			} else {
				answers[i] = mfa
			}
		}
		return answers, nil
	}
}

// dialInteractive 建立 SSH 连接，键盘交互（MFA）提示通过 write 回传，答案从 authCh 读。
// 与 connect() 的区别：非密码提示不再用固定 mfa 值，而是等前端应答（interactive）。
func dialInteractive(host string, port int, user, password, key, passphrase, connID string, write func(wsMsg), authCh <-chan wsMsg) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	if key != "" {
		signer, err := loadKey(key, passphrase)
		if err != nil {
			return nil, fmt.Errorf("加载私钥: %w", err)
		}
		config.Auth = append(config.Auth, ssh.PublicKeys(signer))
	}
	if password != "" {
		seq := 0
		config.Auth = append(config.Auth, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i, q := range questions {
				lq := strings.ToLower(q)
				if strings.Contains(lq, "password") || strings.Contains(lq, "密码") || strings.Contains(lq, "口令") {
					answers[i] = password
				} else {
					seq++
					nonce := fmt.Sprintf("%s-%d", connID, seq)
					write(wsMsg{Type: "auth-prompt", ConnID: connID, Nonce: nonce, Name: user, Instructions: instruction, Prompt: q, Echo: echos[i]})
					select {
					case a := <-authCh:
						if a.Nonce == nonce {
							answers[i] = a.Value
						}
					case <-time.After(120 * time.Second):
						return nil, fmt.Errorf("MFA 认证超时")
					}
				}
			}
			return answers, nil
		}))
		config.Auth = append(config.Auth, ssh.Password(password))
	}
	return dialSSH(fmt.Sprintf("%s:%d", host, port), config)
}

// handleForward 一条转发隧道的双向拷贝（本地 ↔ 远端）。
func handleForward(local net.Conn, client *ssh.Client, remoteAddr string) {
	defer local.Close()
	remote, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("转发通道失败: %v", err)
		return
	}
	defer remote.Close()
	go func() { _, _ = io.Copy(remote, local) }()
	_, _ = io.Copy(local, remote)
}

// loadKey 加载私钥：path 是文件路径；若内容里含 "-----BEGIN" 则视为 PEM 内容本身。
func loadKey(pathOrContent, passphrase string) (ssh.Signer, error) {
	var keyBytes []byte
	if strings.Contains(pathOrContent, "-----BEGIN") {
		keyBytes = []byte(pathOrContent)
	} else {
		var err error
		keyBytes, err = os.ReadFile(pathOrContent)
		if err != nil {
			return nil, err
		}
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
	}
	return ssh.ParsePrivateKey(keyBytes)
}
