package servicehost

import (
	"context"
	"errors"
)

// Control と ServiceState は SCM adapter の内部境界で、製品 IPC ではない。
type Control uint8

const (
	Ignore Control = iota
	Stop
	Shutdown
	Interrogate
)

type ServiceState string

const (
	StartPending ServiceState = "start-pending"
	Running      ServiceState = "running"
	Stopped      ServiceState = "stopped"
	StopPending  ServiceState = "stop-pending"
)

// Serve は停止要求を cancellation へ変換し、所有する読取処理の完了を待つ。
// 状態出力は最新1件へ集約し、受信者の停止で cancellation を塞がない。
// cancellation は協調的。読取側の hang や OS sleep を強制終了できる保証はない。
// 正常 return はこの読取処理の終了だけで、Go Close/SSH/Job の終了証明ではない。
func Serve(host *Host, controls <-chan Control, states chan<- ServiceState) error {
	return serve(host, controls, states, func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
}

// SCM の型をこの同じ loop へ直接写し、bridge worker や無制限 queue を作らない。
func serve[Request, Status any](host *Host, requests <-chan Request, statuses chan<- Status, decode func(Request) Control, encode func(ServiceState) Status) error {
	if host == nil {
		host = New(nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	state, pending, stopping := StartPending, true, false
	for {
		var out chan<- Status
		if pending {
			out = statuses
		}
		select {
		case out <- encode(state):
			pending = false
		case request, open := <-requests:
			command := Stop
			if open {
				command = decode(request)
			} else {
				requests = nil
			}
			switch command {
			case Stop, Shutdown:
				cancel()
				state, pending, stopping = StopPending, true, true
			case Interrogate:
				pending = true
			}
		case err := <-done:
			if stopping && errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}
