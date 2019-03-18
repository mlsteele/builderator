package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

type StatusBar struct{ Port int }

func NewStatusBar(port int) *StatusBar { return &StatusBar{Port: port} }

const (
	StatusBarWhite       = "white"
	StatusBarRed         = "red"
	StatusBarOrange      = "orange"
	StatusBarYellow      = "yellow"
	StatusBarGreen       = "green"
	StatusBarCyan        = "cyan"
	StatusBarBlue        = "blue"
	StatusBarPurple      = "purple"
	StatusBarBlack       = "black"
	StatusBarQuestion    = "question"
	StatusBarExclamation = "exclamation"
)

func (s *StatusBar) Set(ctx context.Context, style string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Second)
	}
	dialer := net.Dialer{
		Deadline: deadline,
	}
	conn, err := dialer.DialContext(ctx, "udp4", fmt.Sprintf(":%v", s.Port))
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	_, err = conn.Write([]byte(style))
	return err
}
