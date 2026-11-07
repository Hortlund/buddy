package localconfig

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

type Connection struct {
	Server      string `json:"server"`
	Token       string `json:"token"`
	Certificate string `json:"certificate"`
	DeviceID    string `json:"device_id,omitempty"`
}

func ValidateServerURL(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("Enter the server's HTTPS origin, including its port if needed.")
	}
	return nil
}

func (c Connection) Validate() error {
	if err := ValidateServerURL(c.Server); err != nil {
		return err
	}
	if !validToken(c.Token) {
		return errors.New("Invalid connection token.")
	}
	_, err := PinnedTLS([]byte(c.Certificate))
	return err
}

func ReadConnection(path string) (Connection, error) {
	var c Connection
	data, err := readPrivateFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, errors.New("Invalid Buddy connection file.")
	}
	return c, c.Validate()
}

func SaveConnection(path string, c Connection, replace bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	dir, err := PrivateDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(dir, filepath.Base(path)), append(data, '\n'), replace)
}

func ConnectionCertificate(dir string) (string, error) {
	dir, err := PrivateDir(dir)
	if err != nil {
		return "", err
	}
	cert, err := readPrivateFile(filepath.Join(dir, CertificateName))
	return string(cert), err
}

// SavePortableConnection creates a new private credential at a user-chosen
// location. Unlike stored app state, an exported file may live in Downloads.
func SavePortableConnection(path string, c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'), false)
}

func ExportConnection(dir, path, server string) error {
	token, err := ReadToken(filepath.Join(dir, TokenName))
	if err != nil {
		return err
	}
	cert, err := os.ReadFile(filepath.Join(dir, CertificateName))
	if err != nil {
		return err
	}
	return SaveConnection(path, Connection{Server: server, Token: token, Certificate: string(cert)}, false)
}
