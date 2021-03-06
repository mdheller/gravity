/*
Copyright 2018 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package proxy implements a simple network proxy for tests
package proxy

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
)

// New returns a new proxy for the given link
func New(link Link, log log.FieldLogger) *Proxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{
		link:        link,
		FieldLogger: log.WithField("proxy", link.String()),
		teardownCh:  make(chan struct{}),
		doneCh:      ctx.Done(),
		cancel:      cancel,
	}
}

// Proxy defines a link between two endpoints
type Proxy struct {
	log.FieldLogger
	link Link
	// doneCh signals that the connections should be dropped
	// and proxy loop stopped
	doneCh <-chan struct{}
	cancel context.CancelFunc
	// teardownCh signals when the connection cleanup has completed
	// and proxy loop has finished
	teardownCh chan struct{}
}

// Start starts the proxy
func (r *Proxy) Start() error {
	listener, err := r.link.Listen()
	if err != nil {
		return trace.Wrap(err)
	}
	go r.serve(listener)
	r.Info("Proxy started.")
	return nil
}

// Stop stops the proxy and drops all active connections
func (r *Proxy) Stop() {
	r.cancel()
	<-r.teardownCh
	r.Info("Proxy stopped.")
}

// Link allows to build a proxying link between two endpoints.
type Link interface {
	fmt.Stringer
	// Listen returns a listener to the local side of the link
	Listen() (net.Listener, error)
	// Dials dials to the remote side of the link
	Dial() (net.Conn, error)
}

// NetLink links two network endpoints.
// Implements Link
type NetLink struct {
	// Local specifies the local side of the connection
	Local net.Listener
	// Upstream specifies the remote side of the connection
	Upstream string
}

// Listen returns a new listener for the local side of the connection
// Implements Link
func (r NetLink) Listen() (net.Listener, error) {
	return r.Local, nil
}

// Dials dials the remote endpoint.
// Implements Link
func (r NetLink) Dial() (net.Conn, error) {
	conn, err := net.Dial("tcp", r.Upstream)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return conn, nil
}

// Strings provides a textual representation of this link
func (r NetLink) String() string {
	return fmt.Sprintf("netlink(local=%v, upstream=%v)", r.Local.Addr(), r.Upstream)
}

func (r *Proxy) serve(listener net.Listener) {
	defer close(r.teardownCh)
	for {
		c1, err := listener.Accept()
		if err != nil {
			r.Errorf("Failed to accept: %v.", err)
			return
		}
		r.Infof("Accept connection from %v.", c1.RemoteAddr())

		c2, err := r.link.Dial()
		if err != nil {
			r.Errorf("Failed to dial: %v.", err)
			c1.Close()
			return
		}
		r.Infof("Upstream connection to %v.", c2.RemoteAddr())

		errCh := make(chan error, 2)
		go proxyConn(c1, c2, errCh)
		go proxyConn(c2, c1, errCh)
		go r.watchConns(errCh, c1, c2, listener)
	}
}

func (r *Proxy) watchConns(errCh <-chan error, closers ...io.Closer) {
	select {
	case err := <-errCh:
		if err != nil {
			r.Warnf("Failed in proxyConn: %v.", err)
		}
	case <-r.doneCh:
	}
	for _, c := range closers {
		c.Close()
	}
}

func proxyConn(c1 io.Writer, c2 io.Reader, errCh chan<- error) {
	_, err := io.Copy(c1, c2)
	errCh <- err
}
