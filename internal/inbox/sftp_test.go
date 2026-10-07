package inbox

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpServer serves the real filesystem over SFTP on localhost to user
// "rdm" with password "secret". It returns the address and the host key
// as an authorized_keys line.
func sftpServer(t *testing.T) (string, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "rdm" && string(pass) == "secret" {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go serveSFTP(nc, config)
		}
	}()
	return l.Addr().String(), string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

func serveSFTP(nc net.Conn, config *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, config)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, reqs, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range reqs {
				ok := r.Type == "subsystem" && string(r.Payload[4:]) == "sftp"
				r.Reply(ok, nil)
				if ok {
					if srv, err := sftp.NewServer(ch); err == nil {
						srv.Serve()
					}
					ch.Close()
				}
			}
		}()
	}
}

func TestSFTP(t *testing.T) {
	ctx := context.Background()
	addr, hostKey := sftpServer(t)
	dir := t.TempDir()
	// Big enough to need many pipelined reads.
	big := bytes.Repeat([]byte("TIPLOC "), 200_000)
	for name, body := range map[string][]byte{
		"RJTTF123.CIF.gz":      big,
		"darwin/ref_v4.xml.gz": []byte("ref"),
		".partial":             []byte("x"),
		".tmp/RJTTF124.CIF.gz": []byte("x"),
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rawURL := "sftp://rdm@" + addr + filepath.ToSlash(dir)

	s, err := NewSFTP(rawURL, "secret", "", hostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	objects, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objects {
		names = append(names, o.Name)
		if o.Name == "RJTTF123.CIF.gz" && o.Size != int64(len(big)) {
			t.Errorf("size %d, want %d", o.Size, len(big))
		}
		if o.Modified.IsZero() {
			t.Errorf("%s: no modified time", o.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "RJTTF123.CIF.gz,darwin/ref_v4.xml.gz" {
		t.Errorf("listed %s", got)
	}

	r, err := s.Open(ctx, "RJTTF123.CIF.gz")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, big) {
		t.Errorf("read %d bytes (err %v), want %d", len(got), err, len(big))
	}

	// Stopping part way through doesn't wedge the connection.
	r, err = s.Open(ctx, "RJTTF123.CIF.gz")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(r, make([]byte, 10))
	r.Close()
	r, err = s.Open(ctx, "darwin/ref_v4.xml.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(r); string(got) != "ref" {
		t.Errorf("read %q", got)
	}
	r.Close()

	if _, err := s.Open(ctx, "../../etc/passwd"); err == nil {
		t.Error("opened a file outside the folder")
	}
	if s.String() != rawURL {
		t.Errorf("String() = %s", s.String())
	}

	t.Run("wrong host key", func(t *testing.T) {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		signer, _ := ssh.NewSignerFromKey(other)
		s, err := NewSFTP(rawURL, "secret", "", string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(ctx); err == nil {
			t.Error("connected to a server with a different host key")
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		s, err := NewSFTP(rawURL, "nope", "", hostKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(ctx); err == nil {
			t.Error("logged in with the wrong password")
		}
	})
	t.Run("config", func(t *testing.T) {
		for _, c := range []struct{ url, pass, host string }{
			{"sftp://" + addr + "/x", "secret", hostKey},    // no user
			{"sftp://rdm:pw@" + addr + "/x", "", hostKey},   // password in URL
			{"sftp://rdm@" + addr + "/x", "secret", ""},     // no host key
			{"sftp://rdm@" + addr + "/x", "", hostKey},      // no credentials
			{"ftp://rdm@" + addr + "/x", "secret", hostKey}, // wrong scheme
		} {
			if _, err := NewSFTP(c.url, c.pass, "", c.host); err == nil {
				t.Errorf("NewSFTP(%s) accepted", c.url)
			}
		}
		// A known_hosts line works as well as an authorized_keys one.
		if _, err := NewSFTP(rawURL, "secret", "", "[127.0.0.1]:2222 "+hostKey); err != nil {
			t.Error(err)
		}
	})
}
