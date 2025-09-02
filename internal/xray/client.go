package xray

import (
	"context"
	"fmt"
	"strings"
	"time"

	command "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Client 封装 Xray gRPC 的最小能力：AddVLESS / AddVMess / Remove（对所有指定 inbound tags）
type Client struct {
	cc      *grpc.ClientConn
	api     command.HandlerServiceClient
	Tags    []string
	Timeout time.Duration
}

// NewClient 连接 Xray gRPC（明文 h2c）。addr 例：127.0.0.1:1090
func NewClient(addr string, tags []string, timeout time.Duration) (*Client, error) {
	cc, err := grpc.Dial(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), // 直到连上再返回
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		cc:      cc,
		api:     command.NewHandlerServiceClient(cc),
		Tags:    tags,
		Timeout: timeout,
	}, nil
}

func (c *Client) Close() { _ = c.cc.Close() }

// AddVLESS 在所有 inbound tag 上添加 VLESS 用户（普通 VLESS 时 flow 传 ""）
func (c *Client) AddVLESS(email, uuid string, level uint32, flow string) error {
	u := &protocol.User{
		Email: email,
		Level: level,
		Account: serial.ToTypedMessage(&vless.Account{
			Id:   uuid,
			Flow: flow, // 非 Vision 留空
		}),
	}
	return c.addUserAll(u)
}

// AddVMess 在所有 inbound tag 上添加 VMess 用户
func (c *Client) AddVMess(email, uuid string, level uint32) error {
	u := &protocol.User{
		Email: email,
		Level: level,
		Account: serial.ToTypedMessage(&vmess.Account{
			Id: uuid,
		}),
	}
	return c.addUserAll(u)
}

// Remove 在所有 inbound tag 上按 email 删除用户
func (c *Client) Remove(email string) error {
	op := &command.RemoveUserOperation{Email: email}
	typed := serial.ToTypedMessage(op)
	for _, tag := range c.Tags {
		if err := c.alter(tag, typed); err != nil {
			return fmt.Errorf("tag=%s: %w", tag, err)
		}
	}
	return nil
}

// --- 内部辅助 ---

func (c *Client) addUserAll(u *protocol.User) error {
	op := &command.AddUserOperation{User: u}
	typed := serial.ToTypedMessage(op)
	for _, tag := range c.Tags {
		if err := c.alter(tag, typed); err != nil {
			// 已存在则忽略继续
			if alreadyExists(err) {
				continue
			}
			return fmt.Errorf("tag=%s: %w", tag, err)
		}
	}
	return nil
}

func (c *Client) alter(tag string, op *serial.TypedMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	_, err := c.api.AlterInbound(ctx, &command.AlterInboundRequest{
		Tag:       tag,
		Operation: op,
	})
	return err
}

func alreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		msg := strings.ToLower(st.Message())
		return strings.Contains(msg, "already") || strings.Contains(msg, "exists")
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already") || strings.Contains(msg, "exists")
}
