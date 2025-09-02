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

type Client struct {
	cc      *grpc.ClientConn
	api     command.HandlerServiceClient
	Tags    []string
	Timeout time.Duration
}

func NewClient(addr string, tags []string, timeout time.Duration) (*Client, error) {
	cc, err := grpc.Dial(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithIdleTimeout(2*time.Minute),
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

func (c *Client) CheckInbound(tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	_, err := c.api.GetInbound(ctx, &command.GetInboundRequest{Tag: tag})
	return err
}

func (c *Client) AddVLESS(email, uuid string, level uint32, flow string) error {
	u := &protocol.User{
		Email: email,
		Level: level,
		Account: serial.ToTypedMessage(&vless.Account{
			Id:   uuid,
			Flow: flow,
		}),
	}
	return c.addUserAll(u)
}

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

func (c *Client) addUserAll(u *protocol.User) error {
	op := &command.AddUserOperation{User: u}
	typed := serial.ToTypedMessage(op)
	for _, tag := range c.Tags {
		if err := c.alter(tag, typed); err != nil {
			// 已存在则忽略
			if alreadyExists(err) {
				continue
			}
			return fmt.Errorf("tag=%s: %w", tag, err)
		}
	}
	return nil
}

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
	return strings.Contains(strings.ToLower(err.Error()), "already") ||
		strings.Contains(strings.ToLower(err.Error()), "exists")
}
