package main

import (
	"io"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/runner-mei/filetail/follower"
	"github.com/runner-mei/filetail/syslog"
	"github.com/runner-mei/filetail/utils"
)

func logDebugf(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

func logInfof(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

func logErrorf(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

func logTracef(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

func logCriticalf(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}

type Server struct {
	config   *Config
	logger   *syslog.Logger
	registry WorkerRegistry
	stopChan chan struct{}
	stopped  bool
	mu       sync.RWMutex
}

func NewServer(config *Config) *Server {
	return &Server{
		config:   config,
		registry: NewInMemoryRegistry(),
		stopChan: make(chan struct{}),
	}
}

func (s *Server) Start() error {
	if err := s.config.Validate(); err != nil {
		return err
	}

	if !s.config.NoDetach && utils.CanDaemonize {
		utils.Daemonize(s.config.DebugLogFile, s.config.PidFile)
	}

	raddr := net.JoinHostPort(s.config.Destination.Host, strconv.Itoa(s.config.Destination.Port))
	logInfof("Connecting to %s over %s", raddr, s.config.Destination.Protocol)

	var err error
	s.logger, err = syslog.Dial(
		s.config.Hostname,
		s.config.Destination.Protocol,
		raddr, s.config.RootCAs,
		s.config.ConnectTimeout,
		s.config.WriteTimeout,
		s.config.TcpMaxLineLength,
	)
	if err != nil {
		logErrorf("Initial connection to server failed: %v - connection will be retried", err)
	}

	go s.tailFiles()

	for err = range s.logger.Errors {
		logErrorf("Syslog error: %v", err)
	}

	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.stopped {
		s.stopped = true
		s.stopChan <- struct{}{}

		logInfof("Shutting down...")
		s.logger.Close()
	}
}

func (s *Server) closing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.stopped
}

// Tails a single file
func (s *Server) tailOne(file, tag string, whence int) {
	defer s.registry.Remove(file)

	t, err := follower.New(file, follower.Config{
		Reopen: true,
		Offset: 0,
		Whence: whence,
	})

	if err != nil {
		logErrorf("%s", err)
		return
	}

	if tag == "" {
		tag = path.Base(file)
	}

	for {
		select {
		case line, ok := <-t.Lines():
			if !ok {
				if t.Err() != nil {
					logErrorf("%s", t.Err())
				}

				return
			}

			if s.closing() {
				t.Close()
				return
			}

			if d := line.Discarded(); d > 0 {
				logInfof("Discarded %d NULL bytes", d)
			}

			l := line.String()

			if !matchExps(l, s.config.ExcludePatterns) {

				s.logger.Write(syslog.Packet{
					Severity: s.config.Severity,
					Facility: s.config.Facility,
					Time:     time.Now(),
					Hostname: s.logger.ClientHostname,
					Tag:      tag,
					Message:  l,
				})

				logTracef("Forwarding line: %s", l)

			} else {
				logTracef("Not Forwarding line: %s", l)
			}

		case <-s.stopChan:
			t.Close()
			return
		}
	}
}

// Tails files speficied in the globs and re-evaluates the globs
// at the specified interval
func (s *Server) tailFiles() {
	logDebugf("Evaluating globs every %s", s.config.NewFileCheckInterval)
	firstPass := true

	for {
		if s.closing() {
			return
		}

		s.globFiles(firstPass)
		time.Sleep(s.config.NewFileCheckInterval)
		firstPass = false
	}
}

func (s *Server) globFiles(firstPass bool) {
	logDebugf("Evaluating file globs")
	for _, glob := range s.config.Files {

		tag := glob.Tag
		files, err := filepath.Glob(utils.ResolvePath(glob.Path))

		if err != nil {
			logErrorf("Failed to glob %s: %s", glob.Path, err)
		} else if files == nil && firstPass {
			logErrorf("Cannot forward %s, it may not exist", glob.Path)
		}

		for _, file := range files {
			switch {
			case s.registry.Exists(file):
				logDebugf("Skipping %s because it is already running", file)
			case matchExps(file, s.config.ExcludeFiles):
				logDebugf("Skipping %s because it is excluded by regular expression", file)
			default:
				logInfof("Forwarding file: %s", file)

				whence := io.SeekStart

				// don't read the entire file on startup
				if firstPass {
					whence = io.SeekEnd
				}

				s.registry.Add(file)
				go s.tailOne(file, tag, whence)
			}
		}
	}
}

// Evaluates each regex against the string. If any one is a match
// the function returns true, otherwise it returns false
func matchExps(value string, expressions []*regexp.Regexp) bool {
	for _, exp := range expressions {
		if exp.MatchString(value) {
			return true
		}
	}
	return false
}

func main() {
	c, err := NewConfigFromEnv()
	if err != nil {
		if err == ErrUsage {
			os.Exit(0)
		}

		logCriticalf("Failed to configure the application: %s", err)
		os.Exit(1)
	}

	utils.AddSignalHandlers()

	s := NewServer(c)
	if err = s.Start(); err != nil {
		logCriticalf("Failed to start server: %v", err)
		os.Exit(255)
	}
}
