package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// MaxMessageBytes 足够放下 20 MB 订阅的 base64。
const MaxMessageBytes = 32 << 20

// Handler 处理一个请求。
type Handler func(Request) Response

// Serve 在 listener 上一问一答地处理请求，直到 listener 被关闭。
func Serve(listener net.Listener, handle Handler, logf func(string, ...any)) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			logf("accept 失败：%v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
			response := handleConn(conn, handle)
			data, _ := json.Marshal(response)
			data = append(data, '\n')
			if _, err := conn.Write(data); err != nil {
				logf("回复失败：%v", err)
			}
		}()
	}
}

func handleConn(conn net.Conn, handle Handler) Response {
	reader := bufio.NewReaderSize(io.LimitReader(conn, MaxMessageBytes), 64*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Failure("bad-request", "没有读到请求", err.Error())
	}
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		return Failure("bad-request", "无法解析请求", err.Error())
	}
	return handle(request)
}

// ClientError 是 App 侧看到的错误。
type ClientError struct {
	// NotRunning：管道不存在或没人监听——服务没装或没在运行。
	NotRunning bool
	Service    *Error
	Err        error
}

func (e *ClientError) Error() string {
	switch {
	case e.NotRunning:
		return "后台组件没有运行。"
	case e.Service != nil:
		return e.Service.Message
	default:
		return "与后台组件通信失败：" + e.Err.Error()
	}
}

func (e *ClientError) Unwrap() error { return e.Err }

// Client 是 App 侧的客户端。
type Client struct {
	// Dial 默认连到服务的命名管道；测试时可替换。
	Dial func(ctx context.Context) (net.Conn, error)
}

func NewClient() *Client { return &Client{Dial: DialService} }

func (c *Client) Hello(ctx context.Context) (*Hello, error) {
	r, err := c.send(ctx, Request{Method: MethodHello}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if r.Hello == nil {
		return nil, &ClientError{Err: errors.New("响应缺少 hello")}
	}
	return r.Hello, nil
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	return c.status(ctx, Request{Method: MethodStatus}, 5*time.Second)
}

func (c *Client) Start(ctx context.Context, p StartParams) (*Status, error) {
	return c.status(ctx, Request{Method: MethodStart, Start: &p}, 90*time.Second)
}

func (c *Client) Stop(ctx context.Context) (*Status, error) {
	return c.status(ctx, Request{Method: MethodStop}, 30*time.Second)
}

func (c *Client) UpdateSubscription(ctx context.Context, body []byte) error {
	_, err := c.send(ctx, Request{Method: MethodUpdateSubscription, Subscription: body}, 30*time.Second)
	return err
}

func (c *Client) status(ctx context.Context, req Request, timeout time.Duration) (*Status, error) {
	r, err := c.send(ctx, req, timeout)
	if err != nil {
		return nil, err
	}
	if r.Status == nil {
		return nil, &ClientError{Err: errors.New("响应缺少 status")}
	}
	return r.Status, nil
}

func (c *Client) send(ctx context.Context, req Request, timeout time.Duration) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	conn, err := c.Dial(dialCtx)
	dialCancel()
	if err != nil {
		if isNotRunning(err) {
			return nil, &ClientError{NotRunning: true, Err: err}
		}
		return nil, &ClientError{Err: err}
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, &ClientError{Err: err}
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, &ClientError{Err: err}
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, MaxMessageBytes), 64*1024).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, &ClientError{Err: fmt.Errorf("读取响应失败：%w", err)}
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, &ClientError{Err: fmt.Errorf("无法解析响应：%w", err)}
	}
	if !response.OK {
		e := response.Error
		if e == nil {
			e = &Error{Code: "unknown", Message: "后台组件返回了未知错误"}
		}
		return nil, &ClientError{Service: e}
	}
	return &response, nil
}

// CheckHello 确认协议版本一致。
func CheckHello(h *Hello) error {
	if h.IPCVersion != appinfo.IPCVersion {
		return fmt.Errorf("后台组件版本不匹配（协议 %d，需要 %d），请重新运行安装程序", h.IPCVersion, appinfo.IPCVersion)
	}
	return nil
}
