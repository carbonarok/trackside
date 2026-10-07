package inbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP is a folder on an SFTP server, for when the marketplace delivers to
// a server trackside doesn't run on. Each List opens a fresh connection,
// which the Opens that follow it reuse, so a poll every 15 minutes doesn't
// hold a connection open in between.
type SFTP struct {
	addr   string
	root   string
	url    string
	config *ssh.ClientConfig

	// StallTimeout fails a download that receives nothing for this long
	// (default 2 minutes).
	StallTimeout time.Duration

	mu     sync.Mutex
	conn   *ssh.Client
	client *sftp.Client
}

// NewSFTP parses sftp://user@host[:port]/path. The server must present
// hostKey, given as an authorized_keys or known_hosts line (ssh-keyscan
// prints one); there is no trust on first use. It authenticates with the
// password, the private key (OpenSSH or PEM), or both.
func NewSFTP(rawURL, password, privateKey, hostKey string) (*SFTP, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "sftp" || u.Hostname() == "" || u.User.Username() == "" {
		return nil, fmt.Errorf("inbox sftp %q: want sftp://user@host[:port]/path", rawURL)
	}
	if _, ok := u.User.Password(); ok {
		return nil, fmt.Errorf("inbox sftp: put the password in INBOX_SFTP_PASSWORD, not the URL")
	}
	key, err := parseHostKey(hostKey)
	if err != nil {
		return nil, err
	}
	var auth []ssh.AuthMethod
	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("inbox sftp key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("inbox sftp: set INBOX_SFTP_PASSWORD or INBOX_SFTP_KEY")
	}
	// Ask for the pinned key's type, or the server may offer another.
	algos := []string{key.Type()}
	if key.Type() == ssh.KeyAlgoRSA {
		algos = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	root := path.Clean("/" + u.Path)
	return &SFTP{
		addr: net.JoinHostPort(u.Hostname(), port),
		root: root,
		url:  (&url.URL{Scheme: "sftp", User: url.User(u.User.Username()), Host: u.Host, Path: root}).String(),
		config: &ssh.ClientConfig{
			User:              u.User.Username(),
			Auth:              auth,
			HostKeyCallback:   ssh.FixedHostKey(key),
			HostKeyAlgorithms: algos,
			Timeout:           30 * time.Second,
		},
	}, nil
}

func parseHostKey(s string) (ssh.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("inbox sftp: set INBOX_SFTP_HOST_KEY to the server's public key (ssh-keyscan -p PORT HOST)")
	}
	if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s)); err == nil {
		return key, nil
	}
	_, _, key, _, _, err := ssh.ParseKnownHosts([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("inbox sftp host key: %w", err)
	}
	return key, nil
}

func (s *SFTP) String() string { return s.url }

// dial replaces any open connection with a new one.
func (s *SFTP) dial(ctx context.Context) (*sftp.Client, error) {
	s.closeLocked()
	d := net.Dialer{Timeout: s.config.Timeout}
	nc, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, err
	}
	// Bound the handshake too; DialContext only covers TCP.
	nc.SetDeadline(time.Now().Add(s.config.Timeout))
	c, chans, reqs, err := ssh.NewClientConn(nc, s.addr, s.config)
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	conn := ssh.NewClient(c, chans, reqs)
	client, err := sftp.NewClient(conn, sftp.UseConcurrentReads(true))
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.conn, s.client = conn, client
	return client, nil
}

// closeLocked cuts the connection first: closing the SFTP client on its
// own waits for outstanding reads, which never finish on a dead link.
func (s *SFTP) closeLocked() {
	if s.conn != nil {
		s.conn.Close()
	}
	if c := s.client; c != nil {
		go c.Close()
	}
	s.conn, s.client = nil, nil
}

// Close drops the connection, if there is one.
func (s *SFTP) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	return nil
}

// List returns the files under the folder, including in sub-folders.
// Hidden files and folders are skipped. Names are relative to the folder.
func (s *SFTP) List(ctx context.Context) ([]Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	var out []Object
	w := client.Walk(s.root)
	for w.Step() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := w.Err(); err != nil {
			return nil, err
		}
		p, info := w.Path(), w.Stat()
		if strings.HasPrefix(path.Base(p), ".") && p != s.root {
			if info.IsDir() {
				w.SkipDir()
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out = append(out, Object{Name: strings.TrimPrefix(p, s.root+"/"), Size: info.Size(),
			Modified: info.ModTime().UTC().Truncate(time.Second)})
	}
	return out, nil
}

// Open downloads a file to a temporary file and returns that, removed on
// Close. Importing the full timetable takes many minutes; reading it
// straight off the network would hold the connection open all that time,
// and a connection that dies silently would leave the import waiting
// forever. Downloading first takes a minute or so, and a download that
// stops making progress for StallTimeout fails, so the next poll retries.
// Names that would escape the folder are refused.
func (s *SFTP) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	p := path.Join(s.root, name)
	if p != s.root && !strings.HasPrefix(p, strings.TrimSuffix(s.root, "/")+"/") {
		return nil, fmt.Errorf("inbox: %q is outside %s", name, s.root)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client := s.client
	if client == nil {
		var err error
		if client, err = s.dial(ctx); err != nil {
			return nil, err
		}
	}
	f, err := client.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tmp, err := os.CreateTemp("", "inbox-*")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (io.ReadCloser, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}

	// Pipelined reads (WriteTo), watched: if no bytes arrive for
	// StallTimeout, or ctx ends, drop the connection to unblock them.
	w := &progressWriter{w: tmp}
	w.touch()
	done := make(chan error, 1)
	go func() {
		_, err := f.WriteTo(w)
		done <- err
	}()
	stall := s.StallTimeout
	if stall <= 0 {
		stall = 2 * time.Minute
	}
	tick := time.NewTicker(stall / 4)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				s.closeLocked()
				return fail(fmt.Errorf("download %s: %w", name, err))
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return fail(err)
			}
			return &tempFile{tmp}, nil
		case <-ctx.Done():
			s.closeLocked()
			settle(done)
			return fail(ctx.Err())
		case <-tick.C:
			if w.idle() > stall {
				s.closeLocked()
				settle(done)
				return fail(fmt.Errorf("download %s: no data for %s", name, stall))
			}
		}
	}
}

// settle waits a little for an abandoned download to stop. If it doesn't,
// it is left to fail on its own: its file is closed and removed.
func settle(done <-chan error) {
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// progressWriter records when it was last written to.
type progressWriter struct {
	w    io.Writer
	last atomic.Int64
}

func (p *progressWriter) touch()              { p.last.Store(time.Now().UnixNano()) }
func (p *progressWriter) idle() time.Duration { return time.Since(time.Unix(0, p.last.Load())) }
func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.touch()
	return n, err
}

// tempFile is removed when closed.
type tempFile struct{ *os.File }

func (t *tempFile) Close() error {
	err := t.File.Close()
	os.Remove(t.Name())
	return err
}
