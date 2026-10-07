package inbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strings"
	"sync"
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

func (s *SFTP) closeLocked() {
	if s.client != nil {
		s.client.Close()
	}
	if s.conn != nil {
		s.conn.Close()
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

// Open streams a file. Names that would escape the folder are refused.
// Reads are pipelined, since one request at a time is slow over a long
// link.
func (s *SFTP) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	p := path.Join(s.root, name)
	if p != s.root && !strings.HasPrefix(p, strings.TrimSuffix(s.root, "/")+"/") {
		return nil, fmt.Errorf("inbox: %q is outside %s", name, s.root)
	}
	s.mu.Lock()
	client := s.client
	var err error
	if client == nil {
		client, err = s.dial(ctx)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f, err := client.Open(p)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	stop := context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })
	go func() {
		defer stop()
		_, err := f.WriteTo(pw)
		f.Close()
		if errors.Is(err, io.ErrClosedPipe) {
			err = nil // the reader stopped early
		}
		pw.CloseWithError(err)
	}()
	return pr, nil
}
