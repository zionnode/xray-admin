package xray

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Client struct {
	API     command.HandlerServiceClient
	Conn    *grpc.ClientConn
	Tags    []string
	Timeout time.Duration
}

func NewClient(addr string, tags []string, timeout time.Duration) (*Client, error) {
	conn, err := grpc.Dial(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(true),
	)
	if err != nil {
		return nil, err
	}
	api := command.NewHandlerServiceClient(conn)
	return &Client{
		API:     api,
		Conn:    conn,
		Tags:    append([]string(nil), tags...),
		Timeout: timeout,
	}, nil
}

func (c *Client) Close() error {
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}

// ---- High-level helpers ----

func (c *Client) AddVLESS(email, uuid string, level uint32, flow string) error {
	acc := &vless.Account{Id: uuid}
	if strings.TrimSpace(flow) != "" {
		acc.Flow = flow // 只有非空才设置
	}
	u := &protocol.User{
		Email:   email,
		Level:   level,
		Account: serial.ToTypedMessage(acc),
	}
	return c.addUserAll(u)
}

func (c *Client) AddVMess(email, uuid string, level uint32) error {
	acc := &vmess.Account{Id: uuid}
	u := &protocol.User{
		Email:   email,
		Level:   level,
		Account: serial.ToTypedMessage(acc),
	}
	return c.addUserAll(u)
}

func (c *Client) Remove(email string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var errs []string
	for _, tag := range c.Tags {
		_, err := c.API.AlterInbound(ctx, &command.AlterInboundRequest{
			Tag: tag,
			Operation: serial.ToTypedMessage(&command.RemoveUserOperation{
				Email: email,
			}),
		})
		if err != nil {
			st, _ := status.FromError(err)
			errs = append(errs, fmt.Sprintf("tag=%s code=%s err=%v", tag, st.Code(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}
	return nil
}

// ---- Internal helpers ----

func (c *Client) addUserAll(u *protocol.User) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var errs []string
	for _, tag := range c.Tags {
		_, err := c.API.AlterInbound(ctx, &command.AlterInboundRequest{
			Tag: tag,
			Operation: serial.ToTypedMessage(&command.AddUserOperation{
				User: u,
			}),
		})
		if err != nil {
			st, _ := status.FromError(err)
			errs = append(errs, fmt.Sprintf("tag=%s code=%s err=%v", tag, st.Code(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}
	return nil
}