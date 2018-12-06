package retry

import (
	"fmt"
	"istio.io/istio/pkg/log"
	"net"
	"net/http"
	"sync"
	"time"
)

type server struct {
	name  string
	port  int
	mutex sync.Mutex
	cond  *sync.Cond
	ready bool
	l     net.Listener
	count int
}

func newServer(name string) (*server, error) {
	s := &server{
		name: name,
	}
	s.cond = sync.NewCond(&s.mutex)

	if err := s.Start(); err != nil {
		return nil, err
	}
	return s, nil
}

func listen() (net.Listener, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	return NewGracefulListener(l, time.Second * 1), nil
}

func (s *server) Start() error {
	l, err := listen()
	if err != nil {
		return err
	}
	s.l = l

	s.mutex.Lock()
	s.port = l.Addr().(*net.TCPAddr).Port
	fmt.Printf("Application %s Port: %d\n", s.name, s.port)
	s.mutex.Unlock()

	// Add the handler for ready probes.
	handler := http.NewServeMux()
	handler.HandleFunc("/", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		//s.mutex.Lock()
		//defer s.mutex.Unlock()

		if s.name == "be1" && s.l == nil {
			fmt.Println("NM: Got a request!")
		}
		rw.WriteHeader(200)
		s.count++
	}))

	// Start the server
	go func() {
		// Indicate that the server is ready.
		s.notifyReady()

		if err := http.Serve(l, handler); err != nil {
			log.Errorf("App %s exited with err: %s", s.name, err.Error())
		}
	}()

	// Wait for the server to be ready before proceeding
	s.waitReady()
	return nil
}

func (s *server) Close() (err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.l != nil {
		fmt.Println("NM: closing App", s.name)
		err = s.l.Close()
		s.l = nil
		fmt.Println("NM: closed App", s.name, err)
	}
	s.notifyReady()
	return err
}

func (s *server) notifyReady() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.ready {
		s.ready = true
		s.cond.Broadcast()
	}
}

func (s *server) waitReady() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.ready {
		s.cond.Wait()
	}
}

// GetConfig provides access to the configuration for this server.
func (s *server) GetPort() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.port
}
